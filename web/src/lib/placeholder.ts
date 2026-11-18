/**
 * 占位图色板与静态占位（data URI）。
 * 多彩低饱和：紫粉蓝橙等多色光斑组合，低饱和但色相丰富；
 * 动态横幅见 features/ui/GradientBanner（CSS 动效 mesh），本文件提供色板与静态头像占位。
 */

/** 多彩低饱和光斑色组：[底色, ...光斑色]；seed 轮换，同 seed 出图稳定 */
export const MESH_PALETTES: { base: string; blobs: string[] }[] = [
  { base: '#EEF1F8', blobs: ['#A5B4FC', '#F0ABFC', '#93C5FD'] }, // 靛蓝/紫粉/天蓝
  { base: '#F3F0EE', blobs: ['#FDBA9F', '#C4B5FD', '#9EE6D4'] }, // 杏橙/淡紫/薄荷
  { base: '#EFF2EC', blobs: ['#B7E0B8', '#A5C7F5', '#F5C6D6'] }, // 苹果绿/雾蓝/藕粉
  { base: '#F6F0E8', blobs: ['#F7C88F', '#B8AEE8', '#8FD0CB'] }, // 奶橙/灰紫/青绿
]

/** 取 seed 对应色组（同 seed 稳定） */
export function meshPalette(seed: number): { base: string; blobs: string[] } {
  const s = Math.abs(Math.trunc(seed)) || 7
  return MESH_PALETTES[s % MESH_PALETTES.length]
}

/** 渐变圆 + 昵称首字占位（无头像时用；色对取自多彩色组的前两色） */
export function avatarPlaceholder(name: string, seed = 7, size = 80): string {
  const { blobs } = meshPalette(seed)
  const ch = (name.trim()[0] ?? '·').toUpperCase()
  const svg =
    `<svg xmlns="http://www.w3.org/2000/svg" width="${size}" height="${size}" viewBox="0 0 ${size} ${size}">` +
    `<defs><linearGradient id="g" x1="0" y1="0" x2="1" y2="1">` +
    `<stop offset="0" stop-color="${blobs[0]}"/><stop offset="1" stop-color="${blobs[1]}"/>` +
    `</linearGradient></defs>` +
    `<rect width="${size}" height="${size}" rx="${size / 2}" fill="url(#g)"/>` +
    `<text x="${size / 2}" y="${Math.round(size * 0.66)}" font-size="${Math.round(size * 0.42)}" ` +
    `font-family="system-ui,sans-serif" fill="#FFFFFF" opacity="0.92" text-anchor="middle">${ch}</text>` +
    `</svg>`
  return `data:image/svg+xml,${encodeURIComponent(svg)}`
}
