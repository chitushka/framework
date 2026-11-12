import { test, expect } from "@playwright/test"

const development = "http://127.0.0.1:8181"
const production = "https://127.0.0.1:8443"

async function signIn(page, base, username, password) {
  await page.goto(`${base}/dashboard`)
  await expect(page).toHaveURL(`${base}/login`)
  await page.getByLabel("Имя пользователя").fill(username)
  await page.getByLabel("Пароль", { exact: true }).fill(password)
  await page.getByRole("button", { name: "Войти" }).click()
  await expect(page).toHaveURL(`${base}/dashboard`)
  await expect(page.getByRole("heading", { name: "Панель управления" })).toBeVisible()
}

async function signOut(page, base) {
  await page.getByRole("button", { name: "Выйти" }).click()
  await expect(page).toHaveURL(`${base}/login`)
  await page.goto(`${base}/dashboard`)
  await expect(page).toHaveURL(`${base}/login`)
}

test("browser forms, server validation, Vite assets and logout", async ({ page }) => {
  const errors = []
  const assets = []
  page.on("pageerror", (error) => errors.push(error.message))
  page.on("console", (message) => {
    // Invalid login/validation intentionally produce 401/422 navigations.
    if (message.type() === "error" && !message.text().includes("Failed to load resource")) errors.push(message.text())
  })
  page.on("response", (response) => { if (response.url().includes("/assets/")) assets.push(response) })
  await page.goto(`${development}/login`)
  await page.screenshot({ path: "test-results/basic-login.png", fullPage: true })
  await page.getByRole("button", { name: "Войти" }).click()
  await expect(page.getByText("Введите имя пользователя.")).toBeVisible()
  await expect(page.getByText("Введите пароль.", { exact: true })).toBeVisible()
  await page.getByLabel("Имя пользователя").fill("admin")
  await page.getByLabel("Пароль", { exact: true }).fill("wrong-password")
  await page.getByRole("button", { name: "Войти" }).click()
  await expect(page.getByRole("alert")).toContainText("Неверное имя пользователя или пароль")
  await expect(page.getByLabel("Пароль", { exact: true })).toHaveValue("")
  await signIn(page, development, "admin", "changeme")
  await expect(page.locator("html")).toHaveAttribute("data-starter", "basic")
  await expect(page.locator(".app-sidebar")).toHaveCSS("background-color", "rgb(32, 44, 66)")
  await page.screenshot({ path: "test-results/basic-desktop.png", fullPage: true })
  await signOut(page, development)
  expect(assets.some((response) => /\.css$/.test(response.url()))).toBeTruthy()
  expect(assets.some((response) => /\.js$/.test(response.url()))).toBeTruthy()
  expect(assets.every((response) => response.status() === 200)).toBeTruthy()
  expect(errors).toEqual([])
})

test("native forms work without JavaScript on a narrow screen", async ({ browser }) => {
  const context = await browser.newContext({ javaScriptEnabled: false, viewport: { width: 390, height: 844 } })
  const page = await context.newPage()
  try {
    await signIn(page, development, "admin", "changeme")
    expect(await page.locator("body").evaluate((body) => body.scrollWidth)).toBeLessThanOrEqual(390)
    await page.screenshot({ path: "test-results/basic-mobile.png", fullPage: true })
    await signOut(page, development)
  } finally { await context.close() }
})

test("production HTTPS uses configured credentials, Secure cookies and CSRF", async ({ page, context }) => {
  const response = await page.goto(`${production}/login`)
  expect(response.headers()["strict-transport-security"]).toContain("max-age=")
  expect(response.headers()["content-security-policy"]).toContain("form-action 'self'")
  await expect(page.getByText("Локальный пример", { exact: false })).toHaveCount(0)
  await page.getByLabel("Имя пользователя").fill("owner")
  await page.getByLabel("Пароль", { exact: true }).fill("changeme")
  await page.getByRole("button", { name: "Войти" }).click()
  await expect(page.getByRole("alert")).toContainText("Неверное имя пользователя или пароль")
  await signIn(page, production, "owner", "browser-production-password")
  const cookies = await context.cookies(production)
  const session = cookies.find((cookie) => cookie.name === "__Host-basic_session")
  expect(session).toMatchObject({ secure: true, httpOnly: true, sameSite: "Lax", path: "/" })
  // A real cross-origin form must fail CSRF and leave the authenticated session intact.
  await page.route("http://attacker.invalid/", (route) => route.fulfill({ contentType: "text/html", body: `<form action="${production}/logout" method="post"><button>Attack</button></form>` }))
  await page.goto("http://attacker.invalid/")
  const denied = page.waitForResponse((response) => response.url() === `${production}/logout` && response.request().method() === "POST")
  await page.getByRole("button", { name: "Attack" }).click()
  expect((await denied).status()).toBe(403)
  await expect(page.locator("body")).toContainText("Forbidden")
  await page.goto(`${production}/dashboard`)
  await expect(page.getByRole("heading", { name: "Панель управления" })).toBeVisible()
  await signOut(page, production)
})
