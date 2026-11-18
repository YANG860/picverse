import { useRef, useState } from 'react'
import { Camera } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { useUpdateAvatar, useUpdateProfile } from '@/api/hooks'
import { describeError } from '@/api/errors'
import { isValidName } from '@/lib/validate'

const BIO_MAX = 200

interface Props {
  initial: { name: string; bio: string; avatarUrl: string }
  onClose: () => void
  /** 资料+头像全部保存成功后回调（缓存已在 hooks 内失效重拉） */
  onSaved: () => void
}

/** 编辑资料弹层：昵称/简介走 UpdateMyProfile（部分更新），头像走 UploadMedia 直传 + UpdateMyAvatar */
export default function EditProfileDialog({ initial, onClose, onSaved }: Props) {
  const [name, setName] = useState(initial.name)
  const [bio, setBio] = useState(initial.bio)
  const [avatarFile, setAvatarFile] = useState<File | null>(null)
  const [avatarPreview, setAvatarPreview] = useState(initial.avatarUrl)
  const [error, setError] = useState('')
  const fileRef = useRef<HTMLInputElement>(null)

  const updateProfile = useUpdateProfile()
  const updateAvatar = useUpdateAvatar()
  const saving = updateProfile.isPending || updateAvatar.isPending

  const pickAvatar = (file: File | undefined) => {
    if (!file) return
    if (!file.type.startsWith('image/')) {
      setError('头像仅支持图片')
      return
    }
    setAvatarFile(file)
    setAvatarPreview(URL.createObjectURL(file))
    setError('')
  }

  const save = async () => {
    const trimmedName = name.trim()
    const trimmedBio = bio.trim()
    if (!isValidName(trimmedName)) {
      setError('昵称需为 1-64 个字符')
      return
    }
    setError('')
    try {
      // 顺序执行：头像链路（直传+等待处理）先行，资料快速保存在后；任一步失败留在弹层内提示
      if (avatarFile) {
        await updateAvatar.mutateAsync(avatarFile)
      }
      // 部分更新语义：仅传变更字段；无变更不发请求（空 patch 会被下游判 invalid argument）
      const patch = {
        name: trimmedName === initial.name ? undefined : trimmedName,
        bio: trimmedBio === initial.bio ? undefined : trimmedBio,
      }
      if (patch.name !== undefined || patch.bio !== undefined) {
        await updateProfile.mutateAsync(patch)
      }
      onSaved()
    } catch (e) {
      setError(describeError(e))
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4" onClick={saving ? undefined : onClose}>
      <div className="w-full max-w-sm rounded-2xl bg-background p-6 shadow-lg" onClick={(e) => e.stopPropagation()}>
        <h2 className="text-lg font-semibold">编辑资料</h2>

        {/* 头像：点击换图 */}
        <div className="mt-4 flex flex-col items-center gap-2">
          <button type="button" className="group relative" onClick={() => fileRef.current?.click()} disabled={saving}>
            <img
              src={avatarPreview || undefined}
              alt=""
              className="size-20 rounded-full bg-muted object-cover ring-2 ring-primary/20"
              onError={(e) => {
                e.currentTarget.style.visibility = 'hidden'
              }}
            />
            <span className="absolute inset-0 grid place-items-center rounded-full bg-black/35 opacity-0 transition-opacity group-hover:opacity-100">
              <Camera className="size-5 text-white" />
            </span>
          </button>
          <span className="text-xs text-muted-foreground">点击更换头像</span>
          <input ref={fileRef} type="file" accept="image/*" className="hidden" onChange={(e) => pickAvatar(e.target.files?.[0])} />
        </div>

        <div className="mt-4 space-y-2">
          <Label htmlFor="edit-name">昵称</Label>
          <Input id="edit-name" value={name} onChange={(e) => setName(e.target.value)} maxLength={64} placeholder="1-64 个字符" disabled={saving} />
        </div>
        <div className="mt-3 space-y-2">
          <div className="flex items-center justify-between">
            <Label htmlFor="edit-bio">简介</Label>
            <span className="text-xs text-muted-foreground">
              {bio.length}/{BIO_MAX}
            </span>
          </div>
          <Textarea id="edit-bio" value={bio} onChange={(e) => setBio(e.target.value.slice(0, BIO_MAX))} rows={3} placeholder="介绍一下自己吧" disabled={saving} />
        </div>

        {error ? <p className="mt-3 text-sm text-destructive">{error}</p> : null}

        <div className="mt-5 flex justify-end gap-2">
          <Button type="button" variant="outline" onClick={onClose} disabled={saving}>
            取消
          </Button>
          <Button type="button" onClick={() => void save()} disabled={saving}>
            {saving ? '保存中…' : '保存'}
          </Button>
        </div>
      </div>
    </div>
  )
}
