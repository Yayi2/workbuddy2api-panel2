package upstream

import (
	"testing"
	"time"
)

// TestParseSoftRateResetBilingual 重置时间解析必须**同时支持**国内站中文与国际站英文文案。
//
// 真实样本（2026-09 用户实测日志）：
//
//	国际站（www.workbuddy.ai）HTTP 429 + code 6004：
//	  {"code":6004,"msg":"usage exceeds frequency limit, but don't worry,
//	   your usage will reset at 2026-09-14 22:09:42 UTC+8,
//	   alternatively, you can switch to the other models to continue using it."}
//
//	国内站 HTTP 429 + code 6004：
//	  {"code":6004,"msg":"请求过于频繁，将在 2026-09-14 22:09:42 重置"}
//
// 只认中文会让国际站账号的冷却退化成固定 soft_rate（默认 600s）基数——
// 而上游明说的时间可能是十小时后。后果：该账号每 10 分钟被重新选中一次、
// 再次 429、再冷却，反复空转，且永远用不上"切其他模型"的豁免提示。
func TestParseSoftRateResetBilingual(t *testing.T) {
	const wantTS = "2026-09-14 22:09:42"

	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "国际站英文（含 UTC+8 后缀）",
			body: `{"code":6004,"msg":"usage exceeds frequency limit, but don't worry, ` +
				`your usage will reset at 2026-09-14 22:09:42 UTC+8, ` +
				`alternatively, you can switch to the other models to continue using it.","requestId":"x"}`,
			want: true,
		},
		{
			name: "国际站英文（无 UTC 后缀）",
			body: `{"code":6004,"msg":"usage will reset at 2026-09-14 22:09:42"}`,
			want: true,
		},
		{
			name: "国内站中文",
			body: `{"code":6004,"msg":"请求过于频繁，将在 2026-09-14 22:09:42 重置"}`,
			want: true,
		},
		{
			name: "国内站中文（带 UTC+8 后缀）",
			body: `{"code":6004,"msg":"频率超限，将在 2026-09-14 22:09:42 UTC+8 重置"}`,
			want: true,
		},
		{
			name: "非 6004（11140 通用限流）——即使带重置时间也不解析",
			body: `{"code":11140,"msg":"rate limit, will reset at 2026-09-14 22:09:42 UTC+8"}`,
			want: false,
		},
		{
			name: "6004 但无时间文案",
			body: `{"code":6004,"msg":"usage exceeds frequency limit"}`,
			want: false,
		},
		{
			name: "6004 但时间是非法格式",
			body: `{"code":6004,"msg":"will reset at not-a-time UTC+8"}`,
			want: false,
		},
		{
			name: "空 body",
			body: ``,
			want: false,
		},
	}

	for _, c := range cases {
		got, ok := ParseSoftRateReset(c.body)
		if ok != c.want {
			t.Errorf("[%s] ParseSoftRateReset ok = %v, want %v", c.name, ok, c.want)
			continue
		}
		if !c.want {
			continue
		}
		if s := got.Format("2006-01-02 15:04:05"); s != wantTS {
			t.Errorf("[%s] 解析出的时间 = %q, want %q", c.name, s, wantTS)
		}
		// 时区必须固定按 UTC+8 解释（与容器时区无关）。
		if _, off := got.Zone(); off != 8*60*60 {
			t.Errorf("[%s] 时区偏移 = %d 秒, want 28800（UTC+8）", c.name, off)
		}
	}
}

// TestParseSoftRateResetZoneIndependent 解析结果必须是**绝对时刻**，不随进程时区漂移。
//
// 上游文案里的时间固定是 UTC+8（文案自带），若用 time.Local 解释，
// 在 TZ=UTC 的容器里会整体偏移 8 小时，冷却窗口就错位了。
func TestParseSoftRateResetZoneIndependent(t *testing.T) {
	body := `{"code":6004,"msg":"usage will reset at 2026-09-14 22:09:42 UTC+8"}`
	got, ok := ParseSoftRateReset(body)
	if !ok {
		t.Fatal("应能解析")
	}
	// 22:09:42 UTC+8 == 14:09:42 UTC。
	wantUTC := time.Date(2026, 9, 14, 14, 9, 42, 0, time.UTC)
	if !got.Equal(wantUTC) {
		t.Errorf("解析结果 = %s (UTC %s), want %s",
			got.Format(time.RFC3339), got.UTC().Format(time.RFC3339), wantUTC.Format(time.RFC3339))
	}
}

// TestModelRateLimitDetection 6004 识别（模型级限流的判据，决定能否切模型豁免）。
func TestModelRateLimitDetection(t *testing.T) {
	yes := []string{
		`{"code":6004}`,
		`{"code": 6004}`,
		`{"code":"6004"}`,
		`{"code" : 6004 ,"msg":"x"}`,
	}
	for _, b := range yes {
		if !IsModelRateLimit(b) {
			t.Errorf("IsModelRateLimit(%q) = false, want true", b)
		}
	}
	no := []string{
		``,
		`{"code":11140}`,
		`{"code":111401}`, // 前缀相似但不是 6004
		`{"code":600}`,
	}
	for _, b := range no {
		if IsModelRateLimit(b) {
			t.Errorf("IsModelRateLimit(%q) = true, want false", b)
		}
	}
}
