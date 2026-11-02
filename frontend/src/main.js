import { createApp } from "vue"
import "@adminkit/core/dist/css/app.css"
import "./styles.scss"
import App from "./App.vue"

createApp(App).mount("#app")
await import("@adminkit/core/dist/js/app.js")
