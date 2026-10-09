import { createPinia } from 'pinia'
import { createApp } from 'vue'
import { setUnauthorizedHandler } from '@/lib/api'
import { loginLocation } from '@/lib/paths'
import { router } from '@/router'
import { useAuthStore } from '@/stores/auth'
import App from './App.vue'
import 'vue-sonner/style.css'
import './style.css'

const app = createApp(App)
const pinia = createPinia()
app.use(pinia)
app.use(router)

// Any 401 (except the /api/me probe) means the session is gone: drop cached
// identity and bounce to the login page, remembering the console page the user
// was on. Public pages (landing, login) just switch to the logged-out state.
setUnauthorizedHandler(() => {
  useAuthStore(pinia).clear()
  const current = router.currentRoute.value
  if (current.meta.public)
    return
  void router.replace(loginLocation(current.fullPath))
})

app.mount('#app')
