import { create } from 'zustand'
import { persist } from 'zustand/middleware'

// 会话令牌存储：access 走请求头，refresh 仅用于换发（MVP 用 localStorage 持久化）
interface AuthState {
  accessToken: string
  refreshToken: string
  setTokens: (accessToken: string, refreshToken: string) => void
  clearTokens: () => void
}

export const useAuthStore = create<AuthState>()(
  persist(
    (set) => ({
      accessToken: '',
      refreshToken: '',
      setTokens: (accessToken, refreshToken) => set({ accessToken, refreshToken }),
      clearTokens: () => set({ accessToken: '', refreshToken: '' }),
    }),
    { name: 'rednote-auth' },
  ),
)
