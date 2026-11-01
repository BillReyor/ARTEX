package notify

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateBytesKeepsValidUTF8(t *testing.T) {
	// This is the most important invariant of this package. Qiwei.com has a **byte** limit, Chinese 3 bytes/word,
	// Any implementation of hard cutting by byte will cut the Chinese characters in half, produce illegal UTF-8 and be rejected by the platform.
	// Use multiple Chinese and English mixed inputs of relatively prime length to hit every possible cut point.
	inputs := []string{
		"中文测试内容",
		"混合 mixed 内容 content",
		"a中b文c测d试e",
		"🔴🟠🟡🔵", // 4-byte emoji, errors are more obvious
		strings.Repeat("漏洞", 100),
	}
	for _, in := range inputs {
		for max := 1; max <= len(in)+2; max++ {
			got := TruncateBytes(in, max)
			if !utf8.ValidString(got) {
				t.Fatalf("Input %q max=%d: output illegal UTF-8 %q", in, max, got)
			}
			if len(got) > max {
				t.Fatalf("Input %q max=%d: Result %d bytes exceed the upper limit", in, max, len(got))
			}
			// Content must not be altered if it is not truncated.
			if len(in) <= max && got != in {
				t.Fatalf("Input %q max=%d: the content is changed without exceeding the limit -> %q", in, max, got)
			}
		}
	}
}

func TestTruncateBytesZeroMeansUnlimited(t *testing.T) {
	long := strings.Repeat("x", 10000)
	if got := TruncateBytes(long, 0); got != long {
		t.Fatal("max=0 should mean no limit")
	}
	if got := TruncateBytes(long, -5); got != long {
		t.Fatal("max<0 should mean no limit")
	}
}

func TestTruncateBytesEllipsisBudget(t *testing.T) {
	// When max is less than the ellipsis itself, it cannot be exceeded by appending an ellipsis.
	got := TruncateBytes("abcdefgh", 1)
	if len(got) > 1 {
		t.Fatalf("When max=1 the result %q length %d exceeds the limit", got, len(got))
	}
	// Normally there should be an ellipsis.
	if got := TruncateBytes("abcdefgh", 5); !strings.HasSuffix(got, ellipsis) {
		t.Fatalf("Expected ellipses, got %q", got)
	}
}

func TestTruncateRunesCountsCharactersNotBytes(t *testing.T) {
	// The difference in caliber with TruncateBytes must be retained: Telegram is limited in length by characters,
	// Using byte caliber will cut the Chinese message to only one third.
	s := "一二三四五六七八九十"
	got := TruncateRunes(s, 5)
	if n := utf8.RuneCountInString(got); n != 5 {
		t.Fatalf("Expected 5 characters, got %d (%q)", n, got)
	}
	// The same string should be significantly shorter in terms of bytes.
	if utf8.RuneCountInString(TruncateBytes(s, 5)) >= 5 {
		t.Fatal("Byte caliber should not produce the same number of characters as character caliber")
	}
}

func TestOneLineCollapsesWhitespace(t *testing.T) {
	got := OneLine("第一行\n\n第二行\t带制表   多空格", 0)
	if strings.ContainsAny(got, "\n\t") {
		t.Fatalf("All whitespace should be collapsed, giving %q", got)
	}
	if strings.Contains(got, "  ") {
		t.Fatalf("Consecutive spaces should not be preserved, getting %q", got)
	}
	// Truncation must still be readable and legal.
	got = OneLine("一二三四五六七八九十", 4)
	if n := utf8.RuneCountInString(got); n != 4 {
		t.Fatalf("Expected 4 characters, got %d (%q)", n, got)
	}
}

func TestTruncateHTMLNeverCutsTagInHalf(t *testing.T) {
	// Directly truncating HTML will cut out fragments such as `<a href="htt`, and the platform will reject the entire message.
	s := `<b>标题</b>正文正文正文<a href="https://example.com/very/long/path">查看详情</a>`
	for max := 1; max <= utf8.RuneCountInString(s)+2; max++ {
		got := TruncateHTML(s, max)
		if n := utf8.RuneCountInString(got); max > 0 && n > max {
			t.Fatalf("max=%d: Result %d character exceeds limit", max, n)
		}
		// There cannot be an unclosed `<` at the end (that is, `<` appears in the last paragraph but there is no `>`).
		if lt := strings.LastIndex(got, "<"); lt >= 0 && !strings.Contains(got[lt:], ">") {
			t.Fatalf("max=%d: The tail label is cut off -> %q", max, got)
		}
	}
}

func TestAssetLineOmitsExcess(t *testing.T) {
	if got := assetLine(nil, 3); got != "" {
		t.Fatalf("If there is no asset, an empty string should be returned and %q is obtained.", got)
	}
	if got := assetLine([]string{"a", "b"}, 3); got != "a, b" {
		t.Fatalf("All items that have not exceeded the limit should be listed, and %q is obtained.", got)
	}
	// When the upper limit is exceeded, the total number must be marked, otherwise readers will not know how many assets are not listed.
	got := assetLine([]string{"a", "b", "c", "d", "e"}, 2)
	if !strings.Contains(got, "(5 total)") {
		t.Fatalf("The total number should be marked 5, getting %q", got)
	}
}

func TestSeverityAndStatusLabels(t *testing.T) {
	if AtLeast("", "low") {
		t.Fatal("Empty level ordinal is 0 and should be blocked by any threshold")
	}
	if !AtLeast("critical", "") {
		t.Fatal("Empty threshold should be released")
	}
	if got := StatusLabel("fixed"); got != "Fixed" {
		t.Fatalf("Unknown state mapping, get %q", got)
	}
	// The unknown status is echoed as it is without any false labels.
	if got := StatusLabel("weird_status"); got != "weird_status" {
		t.Fatalf("The unknown status should be echoed as is, getting %q", got)
	}
}

// An omission pointed out by the TestTruncateHTMLNeverCutsEntity coverage audit: truncation should not only be avoided
// Half-cut tags, but also avoid cut off HTML entities.
//
// After `&amp;` is cut into `&amp`, an entity-only parser may reject the entire message——
// However, extremely long summary messages are already common and the cost is too high.
func TestTruncateHTMLNeverCutsEntity(t *testing.T) {
	s := "aaaa&amp;bbbb&lt;cccc&quot;dddd"
	for max := 1; max <= utf8.RuneCountInString(s)+2; max++ {
		got := TruncateHTML(s, max)
		// Entity fragments "with & but no corresponding ;" must not appear at the end.
		if amp := strings.LastIndex(got, "&"); amp >= 0 && !strings.Contains(got[amp:], ";") {
			t.Fatalf("max=%d: leaving physical fragments %q at the end", max, got[amp:])
		}
		if strings.Contains(got, "&amp\x00") {
			t.Fatalf("max=%d: A deformed entity appears", max)
		}
	}
}
