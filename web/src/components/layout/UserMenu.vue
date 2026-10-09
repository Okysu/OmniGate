<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { House, LogOut, UserRound } from '@lucide/vue'
import { toast } from 'vue-sonner'
import UserAvatar from '@/components/UserAvatar.vue'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { errorMessage } from '@/lib/api'
import { ROLE_LABELS } from '@/lib/format'
import { useAuthStore } from '@/stores/auth'

const auth = useAuthStore()
const router = useRouter()
const loggingOut = ref(false)

async function logout() {
  loggingOut.value = true
  try {
    await auth.logout()
  }
  catch (err) {
    // Session is cleared locally regardless; surface the server error anyway.
    toast.error('退出登录时出错', { description: errorMessage(err) })
  }
  finally {
    loggingOut.value = false
    await router.replace({ name: 'login' })
  }
}
</script>

<template>
  <DropdownMenu v-if="auth.user">
    <DropdownMenuTrigger as-child>
      <Button variant="ghost" size="icon" class="rounded-full" aria-label="用户菜单">
        <UserAvatar :name="auth.user.displayName" :src="auth.user.avatarUrl" />
      </Button>
    </DropdownMenuTrigger>
    <DropdownMenuContent align="end" class="w-60">
      <DropdownMenuLabel class="font-normal">
        <div class="flex items-center gap-3">
          <UserAvatar :name="auth.user.displayName" :src="auth.user.avatarUrl" />
          <div class="min-w-0 flex-1">
            <p class="truncate text-sm font-medium text-foreground">
              {{ auth.user.displayName }}
            </p>
            <p class="text-muted-foreground truncate text-xs">
              {{ auth.user.email ?? '未设置邮箱' }}
            </p>
          </div>
        </div>
        <Badge variant="secondary" class="mt-2">
          {{ ROLE_LABELS[auth.user.role] }}
        </Badge>
      </DropdownMenuLabel>
      <DropdownMenuSeparator />
      <DropdownMenuItem @select="router.push('/console/settings/profile')">
        <UserRound />
        个人设置
      </DropdownMenuItem>
      <DropdownMenuItem @select="router.push('/')">
        <House />
        产品首页
      </DropdownMenuItem>
      <DropdownMenuSeparator />
      <DropdownMenuItem variant="destructive" :disabled="loggingOut" @select="logout">
        <LogOut />
        退出登录
      </DropdownMenuItem>
    </DropdownMenuContent>
  </DropdownMenu>
</template>
