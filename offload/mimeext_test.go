package offload

import (
	"mime"
	"testing"
)

// 媒体类型 → 扩展名推导的判据：覆盖表优先于平台表（键跨机器恒等）、
// 参数与大小写被剥掉、未登记类型走平台表且同进程内稳定、未知类型有中性兜底。

// TestExtForMimeTypeKnown 常见多媒体类型：覆盖表给出权威映射，
// 结果与宿主装了什么 mime.types 无关。
func TestExtForMimeTypeKnown(t *testing.T) {
	cases := map[string]string{
		"image/png":  ".png",
		"image/webp": ".webp",
		"image/gif":  ".gif",
		"image/bmp":  ".bmp",
		"audio/mpeg": ".mp3",
		"audio/wav":  ".wav",
		"audio/ogg":  ".ogg",
		"audio/flac": ".flac",
	}
	for in, want := range cases {
		if got := ExtForMimeType(in); got != want {
			t.Fatalf("%s 应恒得 %s, 实得 %s", in, want, got)
		}
	}
}

// TestExtForMimeTypeOverridesPlatformTable 覆盖表必须抢在平台表之前：
// image/jpeg 在 macOS 的 mime.types 下常得 .jpe，而落盘键跨机器必须一致。
func TestExtForMimeTypeOverridesPlatformTable(t *testing.T) {
	for in, want := range map[string]string{
		"image/jpeg":  ".jpg",
		"image/jpg":   ".jpg", // 非规范别名，归一到同一个权威扩展名
		"audio/x-wav": ".wav",
		"audio/mp3":   ".mp3",
		"image/tiff":  ".tiff", // 平台表里 .tif/.tiff 并列，不钉死就会漂
	} {
		if got := ExtForMimeType(in); got != want {
			t.Fatalf("%s 应恒得 %s, 实得 %s", in, want, got)
		}
	}
	// 如实记录平台表当前给什么，便于日后发现新平台上的分歧面；不断言它必须错。
	if raw, err := mime.ExtensionsByType("image/jpeg"); err == nil && len(raw) > 0 {
		t.Logf("平台表对 image/jpeg 给出 %v，覆盖表已将其钉死为 .jpg", raw)
	}
}

// TestExtForMimeTypeStripsParameters 参数、大小写、首尾空白都要剥掉再查表：
// 带参数的写法在真实消息里极常见，不剥则整条覆盖形同虚设。
func TestExtForMimeTypeStripsParameters(t *testing.T) {
	cases := map[string]string{
		"image/jpeg; charset=utf-8": ".jpg",
		"IMAGE/PNG; q=0.9":          ".png",
		"  audio/wav ;x=y  ":        ".wav",
		"audio/mpeg;":               ".mp3", // 裸分号
		"Image/GIF;":                ".gif",
	}
	for in, want := range cases {
		if got := ExtForMimeType(in); got != want {
			t.Fatalf("%q 应得 %s, 实得 %s", in, want, got)
		}
	}
}

// TestExtForMimeTypePlatformTableStable 未登记覆盖表的类型走平台表。平台表可能
// 给出多个候选，且「取哪个」不该随 stdlib 实现细节漂移——故反复调用必须恒等，
// 且结果必须是能直接拼键的扩展名。
func TestExtForMimeTypePlatformTableStable(t *testing.T) {
	const typ = "text/plain" // 未进覆盖表；Go 内置表就有 .text/.txt 两项
	first := ExtForMimeType(typ)
	if first == FallbackExt {
		t.Fatalf("%s 应能由平台表查到扩展名", typ)
	}
	for i := 0; i < 64; i++ {
		if got := ExtForMimeType(typ); got != first {
			t.Fatalf("第 %d 次调用结果漂移: %s vs %s", i, got, first)
		}
	}
	if _, err := sanitizeExt(first); err != nil {
		t.Fatalf("推导结果必须是可拼键的扩展名: %q", first)
	}
	t.Logf("%s → %s（平台相关，但同进程内恒定）", typ, first)
}

// TestExtForMimeTypeUnknown 未知/不可解析的媒体类型一律中性兜底，不报错也不猜。
func TestExtForMimeTypeUnknown(t *testing.T) {
	for _, in := range []string{
		"application/x-blob-alpha",
		"not-a-media-type",
		"",            // 空
		"   ",         // 全空白
		"; charset=x", // 参数剥完即空
		"  ;  ; ",     // 同上
		"image/",      // 缺子类型
		"image/x-候选",  // 子类型含非 ASCII
	} {
		if got := ExtForMimeType(in); got != FallbackExt {
			t.Fatalf("%q 应兜底为 %s, 实得 %s", in, FallbackExt, got)
		}
	}
}

// TestDerivedExtFeedsStorePath 推导结果要能**直接**当键的一部分用：
// 宿主最省事的用法就是 Blob{Ext: ExtForMimeType(b.MimeType)}，中间不许再加工
// ——任何再加工都会把「单源」重新打散。
func TestDerivedExtFeedsStorePath(t *testing.T) {
	const h = "abcdef0123456789"
	got, err := StorePath(h, ExtForMimeType("image/jpeg; charset=utf-8"))
	if err != nil || got != "blobs/ab/abcdef0123456789.jpg" {
		t.Fatalf("推导结果应直接成键, 实得 %q err=%v", got, err)
	}
	if got, err := StorePath(h, ExtForMimeType("application/x-blob-alpha")); err != nil || got != "blobs/ab/abcdef0123456789.bin" {
		t.Fatalf("未知类型应兜底成键, 实得 %q err=%v", got, err)
	}
}
