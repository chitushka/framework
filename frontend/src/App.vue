<script setup>
import { computed } from "vue"
import AdminLayout from "./components/AdminLayout.vue"
import AuthLayout from "./components/AuthLayout.vue"
import DashboardPage from "./pages/DashboardPage.vue"
import ErrorPage from "./pages/ErrorPage.vue"
import LoginPage from "./pages/LoginPage.vue"
import ProfilePage from "./pages/ProfilePage.vue"
import RecoverPage from "./pages/RecoverPage.vue"
import RegisterPage from "./pages/RegisterPage.vue"

const routes = {
  "/": { component: DashboardPage, title: "Панель управления" },
  "/profile": { component: ProfilePage, title: "Профиль" },
  "/login": { component: LoginPage, title: "Вход", auth: true },
  "/register": { component: RegisterPage, title: "Регистрация", auth: true },
  "/recover": { component: RecoverPage, title: "Восстановление пароля", auth: true },
  "/500": { component: ErrorPage, title: "Ошибка сервера", auth: true, props: { code: 500 } },
}

const current = computed(() => routes[window.location.pathname] ?? {
  component: ErrorPage,
  title: "Страница не найдена",
  auth: true,
  props: { code: 404 },
})
</script>

<template>
  <component :is="current.auth ? AuthLayout : AdminLayout" :title="current.title">
    <component :is="current.component" v-bind="current.props" />
  </component>
</template>
