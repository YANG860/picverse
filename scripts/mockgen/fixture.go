// 文案合成：基于 wallhaven 条目（tags/category/resolution/colors）生成
// 小红书风格的中文昵称/简介/标题/正文/评论/话题标签，喂给 BFF 各接口。
// 词库/模板库全在 pools.go；本文件只做渲染逻辑。
// 模板用命名占位符 + Replacer 渲染，模板可随意增删而不受 Sprintf 参数个数约束；
// 占位符全集见 renderText。
package main

import (
	"fmt"
	"math/rand"
	"strings"
)

// renderText 用命名占位符渲染模板（未用到的占位符自动忽略）。
// 占位符全集：tag cat res resolution ratio colors tail tagsLine hashtags
//
//	mood topic scene series
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
		"{scene}", randScene(rng),
		"{series}", randSeries(rng),
	)
	return r.Replace(tpl)
}

// randHashtags 话题标签串：标签池 + 分类 + 氛围/主题（限 6 个防刷屏）
func randHashtags(rng *rand.Rand, w whWallpaper, tags []string) string {
	picked := map[string]bool{}
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(strings.TrimPrefix(s, "#"))
		// 话题标签不容空格：多词标签去除内部空格（如 "wooden walkway" → "woodenwalkway"）
		s = strings.ReplaceAll(s, " ", "")
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

func randScene(rng *rand.Rand) string {
	return scenePool[rng.Intn(len(scenePool))]
}

func randSeries(rng *rand.Rand) string {
	return seriesPool[rng.Intn(len(seriesPool))]
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
