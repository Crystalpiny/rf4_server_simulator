package gameserver

import (
	"math"
	"strings"
)

// fishFightProfile 是一次搏鱼中鱼种与体重共同决定的动态参数。
// 拉力与曲线缩放已改官方口径（f8 逐鱼种 K 表 + 曲线纯 uniform，见
// specs/changes/fish-pull-k-table/spec.md），profile 只剩体力池一路；
// StaminaScale 作用于 27.f5 的体力池，逐鱼种表属 P2。
type fishFightProfile struct {
	StaminaScale float64
	SizeRatio    float64
}

type fishFightArchetype struct {
	Stamina   float64
	CurvePool string
}

var fishFightArchetypes = map[string]fishFightArchetype{
	"default":  {Stamina: 1.00, CurvePool: "crucian"},
	"ambush":   {Stamina: 0.95, CurvePool: "perch"},
	"salmonid": {Stamina: 1.28, CurvePool: "e.chub"},
	"cyprinid": {Stamina: 1.38, CurvePool: "c.carp"},
	"catfish":  {Stamina: 1.65, CurvePool: "e.chub"},
	"sturgeon": {Stamina: 1.75, CurvePool: "e.chub"},
	"small":    {Stamina: 0.72, CurvePool: "c.roach"},
	"marine":   {Stamina: 1.18, CurvePool: "perch"},
	"eel":      {Stamina: 0.92, CurvePool: "a.sleeper"},
	"apex":     {Stamina: 1.55, CurvePool: "e.chub"},
}

func fishFightArchetypeFor(species string) fishFightArchetype {
	sp := strings.ToLower(strings.TrimSpace(species))
	has := func(parts ...string) bool {
		for _, part := range parts {
			if strings.Contains(sp, part) {
				return true
			}
		}
		return false
	}
	switch {
	case has("shark", "katran", "lamna", "cetorhinus", "somniosus"):
		return fishFightArchetypes["apex"]
	case has("catfish", "burbot", "bullhead", "wels", "som"):
		return fishFightArchetypes["catfish"]
	case has("sturgeon", "beluga", "kaluga", "sterlet", "osetr"):
		return fishFightArchetypes["sturgeon"]
	case has("salmon", "trout", "char", "taimen", "lenok", "nelma", "omul",
		"muksun", "peled", "ripus", "vendace", "smelt", "keta", "nerka", "golets", "grayling"):
		return fishFightArchetypes["salmonid"]
	case has("pike", "zander", "walleye", "perch", "barsch", "orfe", "asp",
		"ruffe", "sander"):
		return fishFightArchetypes["ambush"]
	case has("eel", "lamprey", "conger"):
		return fishFightArchetypes["eel"]
	case has("carp", "bream", "tench", "barbel", "buffalo", "karasekarp"):
		return fishFightArchetypes["cyprinid"]
	case has("roach", "rudd", "dace", "bleak", "gudgeon", "minnow",
		"vendace", "sabrefish", "shemaya", "zubatka", "vobla", "stickleback"):
		return fishFightArchetypes["small"]
	case has("cod", "herring", "mackerel", "makrel", "tuna", "thynnus",
		"halibut", "kambala", "coalfish", "pollachius", "merluza", "molva",
		"hake", "saithe", "turbot", "flounder", "scorpius", "zoarces",
		// 词表修正（搏鱼脱钩方案 §五C）：piksha 实测为海水鲽科，此前被显式
		// 列在 ambush 关键词表里（淡水伏击组；不是子串匹配——has 是
		// strings.Contains，"piksha" 并不含 "pike"）；menek/merlang 为海图鱼种。
		// agar/warm 是旧词表命中、静态库中只在淡水图出现，当前为行为兼容保留
		// （见 chart-roster-marine-group spec）。piksha 的组归属移动对行为中性
		// （ambush 与 marine 的 CurvePool 同为 perch，f5/f6 已有显式表），它的
		// 实际修复来自官方 statics 落表。
		"merlang", "menek", "piksha", "agar", "warm"):
		return fishFightArchetypes["marine"]
	default:
		return fishFightArchetypes["default"]
	}
}

// fightPhaseTensionScale / fightPhaseDurationScale 是 3166958 之前的分阶段
// 节奏表。它并非官方逐帧真值，但能恢复“冲刺—拉扯—休息”的阶段差异；
// 由 fight_phase_scales 开关控制，默认关闭以保持官方 uniform 口径。
func fightPhaseTensionScale(stage uint32) float64 {
	switch stage {
	case 300:
		return 1.36
	case 301:
		return 1.20
	case 302:
		return 1.78
	case 306:
		return 0.42
	case 101:
		return 1.66
	case 102:
		return 1.42
	case 103:
		return 1.16
	case 104:
		return 0.62
	case 105:
		return 0.30
	default:
		return 0.92
	}
}

func fightPhaseDurationScale(stage uint32) float64 {
	switch stage {
	case 302:
		return 0.82
	case 101:
		return 0.72
	case 102:
		return 0.78
	case 103:
		return 1.18
	case 104:
		return 1.35
	case 105, 306:
		return 1.55
	default:
		return 1.0
	}
}

func (s *Server) fishFightProfileFor(f *RolledFish) fishFightProfile {
	arch := fishFightArchetypeFor("")
	if f != nil {
		arch = fishCurveArchetypeFor(f.Species, s.fishMarineGroupHit(f.Species))
	}
	ratio := 0.5
	if f != nil {
		switch f.Grade {
		case 1:
			ratio = 0.25
		case 2:
			ratio = 0.50
		case 3:
			ratio = 0.75
		case 4:
			ratio = 1.0
		}
	}
	if f != nil && f.WeightG > 0 && s != nil && s.DB != nil {
		var maxG int64
		if err := s.DB.SQL.QueryRowContext(s.dbCtx(),
			`SELECT COALESCE(max_g,0) FROM species_weight WHERE fish_key=?`,
			f.Species).Scan(&maxG); err == nil && maxG > 0 {
			ratio = float64(f.WeightG) / float64(maxG)
		}
	}
	ratio = math.Max(0.03, math.Min(1.0, ratio))
	root := math.Sqrt(ratio)
	return fishFightProfile{
		StaminaScale: arch.Stamina * (0.66 + 0.58*root),
		SizeRatio:    ratio,
	}
}
