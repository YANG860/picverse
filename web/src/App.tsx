import type { ReactNode } from 'react'
import { BrowserRouter, Navigate, Route, Routes, useLocation } from 'react-router-dom'
import LoginPage from '@/features/auth/LoginPage'
import RegisterPage from '@/features/auth/RegisterPage'
import FeedPage from '@/pages/FeedPage'
import HomePage from '@/pages/HomePage'
import MockFeedPage from '@/pages/mock/MockFeedPage'
import MockPostPage from '@/pages/mock/MockPostPage'
import MockProfilePage from '@/pages/mock/MockProfilePage'
import MockPublishPage from '@/pages/mock/MockPublishPage'
import PostPage from '@/pages/PostPage'
import PublishPage from '@/pages/PublishPage'
import SearchPage from '@/pages/SearchPage'
import UserProfilePage from '@/pages/UserProfilePage'
import { useAuthStore } from '@/stores/auth'

/** 路由守卫：无 access token 一律去登录页，并记住来源页用于登录后回跳 */
function RequireAuth({ children }: { children: ReactNode }) {
  const hasToken = useAuthStore((s) => Boolean(s.accessToken))
  const location = useLocation()
  return hasToken ? children : <Navigate to="/login" replace state={{ from: location.pathname }} />
}

export default function App() {
  return (
    <BrowserRouter>
      <Routes>
        <Route path="/login" element={<LoginPage />} />
        <Route path="/register" element={<RegisterPage />} />
        {/* 真实 API 页面 */}
        <Route
          path="/"
          element={
            <RequireAuth>
              <FeedPage />
            </RequireAuth>
          }
        />
        <Route
          path="/search"
          element=
            {<RequireAuth>
              <SearchPage />
            </RequireAuth>}
        />
        <Route
          path="/post/:id"
          element={
            <RequireAuth>
              <PostPage />
            </RequireAuth>
          }
        />
        <Route
          path="/publish"
          element={
            <RequireAuth>
              <PublishPage />
            </RequireAuth>
          }
        />
        <Route
          path="/user/:uid"
          element={
            <RequireAuth>
              <UserProfilePage />
            </RequireAuth>
          }
        />
        <Route
          path="/account"
          element={
            <RequireAuth>
              <HomePage />
            </RequireAuth>
          }
        />
        {/* mock 路由：纯 UI + 合成数据，仅用于视觉效果验证，不接 API */}
        <Route path="/mock" element={<MockFeedPage />} />
        <Route path="/mock/post/:id" element={<MockPostPage />} />
        <Route path="/mock/profile" element={<MockProfilePage />} />
        <Route path="/mock/profile/:uid" element={<MockProfilePage />} />
        <Route path="/mock/publish" element={<MockPublishPage />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
    </BrowserRouter>
  )
}
