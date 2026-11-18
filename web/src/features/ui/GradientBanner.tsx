import { useMemo } from 'react'
import { meshPalette } from '@/lib/placeholder'

/**
 * 动态流光渐变横幅（mesh 光斑 + 超慢漂移动画）：
 * 3-4 个大半径柔光斑叠加，20-30s 级缓速漂移（几乎察觉不到的“活”）；
 * 尊重 prefers-reduced-motion（减弱动效时静止）。色组/位置/相位按 seed 稳定。
 */
export default function GradientBanner({ seed, className = '' }: { seed: number; className?: string }) {
  const { base, blobs } = meshPalette(seed)
  // 光斑布局：位置/大小/动画时长/负延迟错相，全部由 seed 派生（同 seed 稳定）
  const spots = useMemo(() => {
    const s = Math.abs(Math.trunc(seed)) || 7
    const rnd = (i: number) => {
      let t = (s + i * 2654435761) >>> 0
      t = Math.imul(t ^ (t >>> 15), 2246822519)
      return ((t ^ (t >>> 13)) >>> 0) / 4294967296
    }
    return blobs.map((color, i) => ({
      color,
      left: 6 + rnd(i * 4) * 62, // %
      top: 8 + rnd(i * 4 + 1) * 42, // %
      size: 52 + rnd(i * 4 + 2) * 34, // %（相对容器宽）
      duration: 22 + rnd(i * 4 + 3) * 10, // s
      delay: -rnd(i * 4 + 4) * 18, // s（负延迟随机入场相位）
    }))
  }, [blobs, seed])

  return (
    <div className={`relative isolate overflow-hidden ${className}`} style={{ background: base }}>
      {spots.map((b, i) => (
        <div
          key={i}
          className="gradient-blob absolute rounded-full"
          style={{
            left: `${b.left}%`,
            top: `${b.top}%`,
            width: `${b.size}%`,
            aspectRatio: '1',
            background: `radial-gradient(circle, ${b.color} 0%, ${b.color}00 70%)`,
            animationDuration: `${b.duration}s`,
            animationDelay: `${b.delay}s`,
            filter: 'blur(48px)',
            willChange: 'transform',
          }}
        />
      ))}
    </div>
  )
}
