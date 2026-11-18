// 合成数据（仅 mock 路由使用，不接真实 API）：
// 图片为本地生成的渐变 SVG data URI（离线可靠，按 seed 稳定出图）；
// 数据用 mulberry32 伪随机，同一 seed 刷新页面视觉效果一致，便于反复调样式。
import type { CommentView, FeedTab, PostCard, PostDetail, PostStat, ProfileView, UserBrief } from '@/features/models'

/** 渐变占位图（data URI，不依赖网络）；hue 由 seed 决定，同 seed 出图稳定。
 * 低饱和莫兰迪系（饱和 ~22%/20%）：用于 mock 内容图（模拟照片），避免高饱和渐变抢视觉。 */
export function gradientImage(seed: number, w: number, h: number): string {
  const hue = (seed * 47) % 360
  const svg =
    `<svg xmlns="http://www.w3.org/2000/svg" width="${w}" height="${h}" viewBox="0 0 ${w} ${h}">` +
    `<defs><linearGradient id="g" x1="0" y1="0" x2="1" y2="1">` +
    `<stop offset="0" stop-color="hsl(${hue},22%,86%)"/>` +
    `<stop offset="1" stop-color="hsl(${(hue + 48) % 360},20%,72%)"/>` +
    `</linearGradient></defs><rect width="${w}" height="${h}" fill="url(#g)"/></svg>`
  return `data:image/svg+xml,${encodeURIComponent(svg)}`
}

/** mulberry32：轻量可复现伪随机 */
function mulberry32(seed: number): () => number {
  let a = seed >>> 0
  return () => {
    a = (a + 0x6d2b79f5) >>> 0
    let t = Math.imul(a ^ (a >>> 15), 1 | a)
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}

function pick<T>(rand: () => number, items: readonly T[]): T {
  return items[Math.floor(rand() * items.length)]
}

const COVER_WIDTH = 400
/** 封面高宽比池（瀑布流错落感） */
const ASPECTS = [4 / 5, 3 / 4, 1, 4 / 3, 3 / 5, 5 / 7, 16 / 10] as const

const NAMES = ['小鹿同学', '阿茶的日常', '一只柚子', '晚风慢慢', '盐系少女', '山野雾岛', '半糖布丁', '阿啾不睡觉', '林间有风', '甜豆花'] as const

const TITLES = [
  '分享我的秋日穿搭🍂 这件毛衣真的绝了',
  '超简单的空气炸锅食谱，新手零失败',
  '周末 Citywalk 路线合集｜走完这条街就是秋天',
  '租房改造｜300 块把出租屋变成奶油小屋',
  '打工人便当 Week｜一周不重样的快手菜',
  '素人也能学会的日常妆容教程✨',
  '旅行清单｜去海边一定要带这几样',
  '健身第 30 天，说说我的真实变化',
  '咖啡角布置分享｜每天最治愈的角落',
  '好物分享｜这些平价好物我回购了 N 次',
  '周末烘焙｜零失败的巴斯克蛋糕配方',
  '养猫人的家｜猫窝也可以很好看',
  '换季护肤｜干皮的本命面霜找到了',
  '拍照技巧｜手机也能拍出氛围感大片',
  '一人食晚餐｜下班 15 分钟搞定',
  '书单分享｜最近读到停不下来的 5 本书',
  '阳台种菜记录｜第一次收获太有成就感',
  '小个子穿搭公式｜显高 10cm 不是梦',
  '露营装备清单｜新手入坑看这一篇',
  '手账入坑指南｜我的桌面布置分享',
  '减脂餐｜好吃不胖的一周食谱',
  '家居好物｜提升幸福感的小东西们',
  '秋游野餐攻略｜出片率超高的野餐布置',
  '通勤穿搭｜简约耐看的上班look',
] as const

const PARAGRAPHS = [
  '先说结论：真的好用！已经连续用了一周多，效果肉眼可见，姐妹们冲就完事了。',
  '整个过程比想象中简单很多，跟着步骤来基本不会翻车，第一次做就成功啦。',
  '踩过的坑都帮大家踩过了，注意事项写在最后，记得看到底哦～',
  '预算有限的宝子可以看看平替方案，效果差不多，性价比超高。',
  '拍照的时候光线很重要，下午三四点的自然光最出片，原图直出无压力。',
  '如果有想看的细节可以评论区告诉我，看到都会回！',
  '这次体验整体打 9 分，扣 1 分是因为发货有点慢，其他都很满意。',
] as const

const REPLIES = [
  '求链接！太好看了吧',
  '已经下单了，坐等收货',
  '请问敏感肌可以用吗？',
  '收藏了，周末就试试',
  '博主太会买了吧！',
  '同款路过，确实好用',
  '这颜色也太温柔了',
  '蹲一个后续反馈',
] as const

const COMMENT_TEXTS = [
  '这也太好看了吧，果断收藏！',
  '跟着做了一次，真的很简单，感谢分享～',
  '请问在哪里买的呀？求个链接',
  '已经安利给闺蜜了哈哈',
  '拍得也太有氛围感了，学到了',
  '同款选手报道！用了三个月了',
  '看完立刻打开购物车',
  '笔记写得好详细，点赞！',
  '想看更多类似的分享',
  '博主的审美真的绝',
] as const

const uid = (n: number) => `10${String(n).padStart(12, '0')}`

function user(n: number): UserBrief {
  return {
    uid: uid(n),
    name: NAMES[n % NAMES.length],
    avatarUrl: gradientImage(n + 100, 160, 160),
  }
}

function stat(rand: () => number): PostStat {
  return {
    likeCount: Math.floor(rand() * 5000),
    favouriteCount: Math.floor(rand() * 2000),
    commentCount: Math.floor(rand() * 300),
    viewCount: Math.floor(rand() * 20000),
  }
}

function media(rand: () => number, seed: number) {
  const aspect = pick(rand, ASPECTS)
  const height = Math.round(COVER_WIDTH / aspect)
  return {
    url: gradientImage(seed, COVER_WIDTH, height),
    width: COVER_WIDTH,
    height,
  }
}

const PER_PAGE = 10
/** 每个 tab 生成 6 页，到底后停止加载 */
export const MAX_PAGE = 6

/** 信息流一页合成卡片（tab + 页码决定 seed，稳定可复现） */
export function listFeed(tab: FeedTab, page: number): PostCard[] {
  const tabSeed = { recommend: 11, hot: 22, following: 33 }[tab]
  return Array.from({ length: PER_PAGE }, (_, i) => {
    const seed = tabSeed * 10000 + page * 100 + i
    const rand = mulberry32(seed)
    return {
      postId: String(seed),
      title: pick(rand, TITLES),
      cover: media(rand, seed),
      author: user(seed % 10),
      stat: stat(rand),
      createdAt: Date.now() - Math.floor(rand() * 30 * 24 * 3600 * 1000),
    }
  })
}

/** 由 postId 反查/合成详情（hash 决定内容，刷新稳定） */
export function getPostDetail(postId: string): PostDetail {
  const seed = Number(postId) || 42
  const rand = mulberry32(seed)
  const mediaCount = 2 + Math.floor(rand() * 3)
  return {
    postId,
    title: pick(rand, TITLES),
    cover: media(rand, seed),
    medias: Array.from({ length: mediaCount }, (_, i) => media(rand, seed + i + 1)),
    author: user(seed % 10),
    stat: stat(rand),
    createdAt: Date.now() - Math.floor(rand() * 14 * 24 * 3600 * 1000),
    fullText: Array.from({ length: 3 + Math.floor(rand() * 4) }, () => pick(rand, PARAGRAPHS)).join('\n\n'),
    relation: { isFollowing: rand() > 0.5, isFollower: false },
  }
}

/** 楼层 + 楼中楼回复（对齐 CommentView 两级语义） */
export function listComments(postId: string): CommentView[] {
  const rand = mulberry32((Number(postId) || 42) + 7)
  return Array.from({ length: 8 }, (_, i) => {
    const rootSeed = (Number(postId) || 42) * 10 + i
    const replyCount = Math.floor(rand() * 3)
    const root: CommentView = {
      commentId: String(rootSeed),
      rootId: String(rootSeed),
      parentId: '',
      user: user(rootSeed % 10),
      content: pick(rand, COMMENT_TEXTS),
      createdAt: Date.now() - Math.floor(rand() * 7 * 24 * 3600 * 1000),
      replyCount,
    }
    if (replyCount > 0) {
      root.replies = Array.from({ length: replyCount }, (_, j) => ({
        commentId: `${rootSeed}-${j}`,
        rootId: String(rootSeed),
        parentId: j === 0 ? String(rootSeed) : `${rootSeed}-${j - 1}`,
        user: user((rootSeed + j + 1) % 10),
        content: pick(rand, REPLIES),
        createdAt: Date.now() - Math.floor(rand() * 3 * 24 * 3600 * 1000),
        replyCount: 0,
        replyToUser: j === 0 ? undefined : user(rootSeed % 10),
      }))
    }
    return root
  })
}

/** 用户主页合成数据（对齐 bff.v1.ProfileView） */
export function getProfile(uid: string): ProfileView {
  const seed = (Number(uid) || 7) + 999
  const rand = mulberry32(seed)
  return {
    brief: user(seed % 10),
    bio: '这个人很酷，什么都没写～\n爱生活，爱分享｜好物/穿搭/日常',
    stat: {
      followers: Math.floor(rand() * 50000),
      followings: Math.floor(rand() * 500),
    },
    relation: { isFollowing: rand() > 0.5, isFollower: rand() > 0.7 },
    likeReceived: Math.floor(rand() * 100000),
  }
}

/** 个人主页笔记/收藏列表（自己视角的笔记含草稿/审核中） */
export function listUserPosts(uid: string, kind: 'notes' | 'favourites', isSelf: boolean): PostCard[] {
  const seed = (Number(uid) || 7) * 31 + (kind === 'notes' ? 5 : 6)
  const rand = mulberry32(seed)
  const target = user((Number(uid) || 7) % 10)
  return Array.from({ length: 12 }, (_, i) => {
    const itemSeed = seed * 100 + i
    const r = mulberry32(itemSeed)
    // 自己视角的“笔记”混入草稿/审核中（对齐 GetMyPosts 语义，驱动角标视觉）
    let status: PostCard['status'] = 'PUBLISHED'
    if (isSelf && kind === 'notes') {
      status = i === 1 ? 'DRAFT' : i === 3 ? 'AUDITING' : 'PUBLISHED'
    }
    return {
      postId: String(itemSeed),
      title: pick(r, TITLES),
      cover: media(r, itemSeed),
      author: target,
      stat: stat(r),
      createdAt: Date.now() - Math.floor(rand() * 180 * 24 * 3600 * 1000),
      status,
    }
  })
}
