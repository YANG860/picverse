// 文案合成：基于 wallhaven 条目（tags/category/resolution/colors）生成
// 小红书风格的中文昵称/简介/标题/正文/评论/话题标签，喂给 BFF 各接口。
// 模板用命名占位符（{tag}/{cat}/{res}...）+ Replacer 渲染，模板可随意增删
// 而不受 Sprintf 参数个数约束；占位符全集见 renderText。
package main

import (
	"fmt"
	"math/rand"
	"strings"
)

var nameAdj = []string{
	"山间", "晚风", "云端", "星野", "南巷", "北岛", "暮色", "晨光", "深海", "空谷",
	"夏日", "冬眠", "春山", "秋原", "薄雾", "极光", "流萤", "落日", "潮汐", "微光",
	"青柠", "海盐", "焦糖", "雾岛", "风铃", "纸鸢", "麦田", "冰川", "琥珀", "云杉",
}

var nameNoun = []string{
	"小鹿", "信笺", "旅人", "猫", "画师", "拾光者", "收藏家", "观察员", "梦想家", "背包客",
	"诗人", "摄影师", "追风者", "守夜人", "造梦师", "手记", "电台", "书房", "星球", "岛屿",
	"色卡", "放映室", "调色盘", "画框", "底片", "驿站", "月台", "灯塔", "气泡水", "晴天伞",
}

var bioPool = []string{
	"壁纸 / 摄影 / 生活美学，随手记录好看的角落。",
	"分享一切让眼睛舒服的东西。",
	"审美是种习惯，这里是练习场。",
	"收集色彩与光影，偶尔碎碎念。",
	"专注桌面美学十年（并没有）。",
	"图源网络，仅做分享，侵删。",
	"慢热，爱山海，爱一切高饱和的蓝。",
	"用壁纸治愈每一个工作日。",
	"喜欢就拿走，记得常来看看。",
	"一个安静的图片搬运工。",
	"日更选手，全靠壁纸续命。",
	"相信色彩有治愈力。",
	"电脑桌面比家里桌面整洁系列。",
	"低产但精选，宁缺毋滥。",
	"把好看的图都存进来，别问，问就是硬盘大。",
}

// titleTpl 标题模板（占位符：tag=随机标签 cat=分类 res=分辨率 topic=主题 mood=氛围）
var titleTpl = []string{
	"今日壁纸分享｜{tag}",
	"{tag}控狂喜！这组 {res} 壁纸太顶了",
	"桌面换新：{tag} {res} 壁纸合集",
	"治愈系壁纸｜{tag}，一眼就心动",
	"{tag} 高清壁纸，{res} 直接拿走",
	"私藏壁纸 {tag}｜建议先码后看",
	"氛围感拉满的{tag}壁纸，谁懂",
	"{tag} × {cat}：这配色我能看一天",
	"换上就不想换系列｜{tag}",
	"今日份壁纸投喂 🎨 {tag}",
	"{topic}主题壁纸｜{tag}",
	"桌面美学｜{tag} 的 {res} 视觉盛宴",
	"谁懂啊！{tag} 壁纸真的越看越上头",
	"收藏向｜{tag} 高清大图合集",
	"{mood}壁纸｜{tag}，{res} 无水印",
	"不允许有人没看过这张 {tag} 壁纸",
}

// textTpl 正文模板（占位符全集见 renderText；多段落 \n\n 分隔）
var textTpl = []string{
	"今天这组壁纸主色调是 {colors}，{resolution} 分辨率，比例 {ratio}。\n\n{tail}\n\n{tagsLine}",
	"又挖到宝了！这张 {cat} 风格的壁纸分辨率 {resolution}，色彩 {colors}。\n\n{tail}\n\n喜欢的自取～{tagsLine}",
	"分享一张最近的心头好：{cat} × {resolution}。\n\n{tail}\n\n{hashtags}",
	"{cat} 风格的壁纸永远不过时。分辨率 {resolution}，主色 {colors}，氛围 {mood}。\n\n{tail}\n\n{tagsLine}",
	"今日主题：{topic}。选了一张 {resolution} 的 {cat} 壁纸，主色 {colors}。\n\n{tail}\n\n{hashtags}",
	"{mood}的一天，配一张 {mood} 的壁纸。{resolution}，{ratio} 比例。\n\n{tail}\n\n{tagsLine}",
	"这张我愿称之为 {topic} 天花板 🏆\n\n参数：{resolution} / {ratio} / {colors}\n\n{tail}\n\n{hashtags}",
	"来点不一样的：{tag} × {mood}。\n\n{tail}\n\n适合场景：办公桌面 / 锁屏 / 平板壁纸。\n\n{tagsLine}",
	"这张 {cat} 壁纸的 {colors} 配色真的绝。\n\n{tail}\n\n取图记得点赞收藏，找图不迷路～{tagsLine}",
	"整理了一波 {topic} 相关的壁纸，先发这张 {resolution} 的。\n\n{tail}\n\n想看更多 {tag} 相关的评论区告诉我。\n\n{hashtags}",
}

var textTail = []string{
	"换上之后桌面瞬间清爽了不少，工作心情都变好了。",
	"适合深色模式的桌面，字都变清楚了。",
	"横屏竖屏都能裁，手机电脑通吃。",
	"低调又不单调，久看不腻。",
	"已经连续用了一周，还没看腻。",
	"配色很高级，做锁屏也好看。",
	"裁切自由度很高，随便截都很能打。",
	"放大看细节也很能打，糊不了。",
	"这种质感的图现在不多见了，且看且珍惜。",
	"个人觉得是近期最佳，不接受反驳（bushi）。",
	"搭配同色系图标主题效果更佳。",
	"眼睛累了看这张，瞬间回血。",
	"放在副屏当装饰画也不错。",
	"冷色调控狂喜，蓝得很纯粹。",
	"构图工整，中间留白放图标正好。",
}

// moodPool 壁纸氛围词（正文/标题/元数据可选文本）
var moodPool = []string{
	"治愈系", "清冷", "热烈", "复古", "极简", "赛博朋克", "蒸汽波", "奶油风",
	"暗黑系", "小清新", "电影感", "胶片感", "慵懒", "静谧", "梦幻", "硬核",
}

// topicPool 壁纸主题词（正文/标题/元数据可选文本）
var topicPool = []string{
	"桌面美学", "配色灵感", "自然风光", "二次元", "科幻宇宙", "城市夜景", "极简主义",
	"怀旧游戏", "插画艺术", "太空探索", "海洋深蓝", "四季流转", "光影实验", "赛博都市",
	"山川湖海", "像素艺术",
}

var commentPool = []string{
	"这张的配色绝了！",
	"求 4K 原图🙏",
	"已保存，感谢分享～",
	"这个色调好舒服，眼睛做 spa。",
	"换上了！同事都问我在哪找的。",
	"收藏了，正好缺一张桌面。",
	"哇这个构图，太会拍了。",
	"码住，改天换上。",
	"质感也太好了吧，爱了爱了。",
	"手机电脑各换一张，完美。",
	"每次刷到都忍不住存图。",
	"这色彩管理可以啊，和我的主题很搭。",
	"请问有竖版吗？",
	"终于等到有人发这系列了！",
	"氛围感直接拉满。",
	"这张做锁屏绝配。",
	"一眼心动，不解释。",
	"博主审美在线，关注了。",
	"我的桌面已经换上了，好评！",
	"这也太清晰了吧，舒服了。",
	"求出一期同色系合集🥺",
	"就是这种感觉！找了好久。",
	"已加入我的壁纸轮换豪华套餐。",
	"配色能出个 hex 吗，想做主题🎨",
	"看完立刻换壁纸，真香。",
	"这系列都好能打，一张没落下。",
	"建议置顶，别沉了。",
	"暗色模式用户狂喜。",
	"这图我能看一整天不带腻的。",
	"太会找了，respect 🫡",
}

var replyPool = []string{
	"同感！真的好看。",
	"确实，越看越喜欢。",
	"+1，已加入常用壁纸库。",
	"哈哈谢谢喜欢～",
	"眼光一致🤝",
	"换上之后体验极佳。",
	"是的，久看不腻那种。",
	"安排上了！",
	"哈哈我也是这么想的。",
	"英雄所见略同。",
	"这就去换！",
	"欢迎常来逛逛～",
}

// renderText 用命名占位符渲染模板（未用到的占位符自动忽略）。
// 占位符全集：tag cat res resolution ratio colors tail tagsLine hashtags mood topic
func renderText(tpl string, rng *rand.Rand, w whWallpaper, tags []string) string {
	tag := categoryCN(w.Category)
	if len(tags) > 0 {
		tag = tags[rng.Intn(len(tags))]
	}
	colors := "多色"
	if len(w.Colors) > 0 {
		colors = strings.Join(w.Colors[:min(3, len(w.Colors))], " / ")
	}
	tagsLine := strings.Join(tags, "、")
	if tagsLine == "" {
		tagsLine = categoryCN(w.Category)
	}
	r := strings.NewReplacer(
		"{tag}", tag,
		"{cat}", categoryCN(w.Category),
		"{res}", w.Resolution,
		"{resolution}", w.Resolution,
		"{ratio}", w.Ratio,
		"{colors}", colors,
		"{tail}", textTail[rng.Intn(len(textTail))],
		"{tagsLine}", tagsLine,
		"{hashtags}", randHashtags(rng, w, tags),
		"{mood}", randMood(rng),
		"{topic}", randTopic(rng),
	)
	return r.Replace(tpl)
}

// randHashtags 话题标签串：标签池 + 分类 + 氛围/主题（限 6 个防刷屏）
func randHashtags(rng *rand.Rand, w whWallpaper, tags []string) string {
	picked := map[string]bool{}
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(strings.TrimPrefix(s, "#"))
		if s == "" || picked[s] || len(out) >= 6 {
			return
		}
		picked[s] = true
		out = append(out, "#"+s)
	}
	// 标签优先（随机顺序取前几个），再补分类/氛围/主题
	shuffled := append([]string(nil), tags...)
	rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
	for _, t := range shuffled {
		add(t)
	}
	add(categoryCN(w.Category))
	if rng.Intn(2) == 0 {
		add(randMood(rng))
	} else {
		add(randTopic(rng))
	}
	return strings.Join(out, " ")
}

// randName 生成中文昵称（形容词 + 名词，撞名自动加序号消歧）
func randName(rng *rand.Rand, idx int) string {
	return fmt.Sprintf("%s%s%d", nameAdj[rng.Intn(len(nameAdj))], nameNoun[rng.Intn(len(nameNoun))], idx)
}

// bioFor 确定性简介：按邮箱哈希选池，重跑同值（幂等覆盖不抖动）
func bioFor(email string) string {
	return bioPool[fnv32(email)%uint32(len(bioPool))]
}

func randMood(rng *rand.Rand) string {
	return moodPool[rng.Intn(len(moodPool))]
}

func randTopic(rng *rand.Rand) string {
	return topicPool[rng.Intn(len(topicPool))]
}

// categoryCN wallhaven 分类 → 中文
func categoryCN(c string) string {
	switch c {
	case "general":
		return "风景"
	case "anime":
		return "动漫"
	case "people":
		return "人物"
	}
	return "精选"
}

// randTitle 随机标题模板渲染
func randTitle(rng *rand.Rand, w whWallpaper, tags []string) string {
	return renderText(titleTpl[rng.Intn(len(titleTpl))], rng, w, tags)
}

// randText 随机正文模板渲染
func randText(rng *rand.Rand, w whWallpaper, tags []string) string {
	return renderText(textTpl[rng.Intn(len(textTpl))], rng, w, tags)
}

func randComment(rng *rand.Rand) string {
	return commentPool[rng.Intn(len(commentPool))]
}

func randReply(rng *rand.Rand) string {
	return replyPool[rng.Intn(len(replyPool))]
}
