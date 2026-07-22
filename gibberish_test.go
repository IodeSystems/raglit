package raglit

import "testing"

func TestIsGibberish(t *testing.T) {
	cases := []struct {
		name string
		po   PageOCR
		want bool
	}{
		{
			name: "clean printed text",
			po: PageOCR{
				Text:           "The quarterly report shows revenue grew twelve percent over the prior period across all regions.",
				MeanConfidence: 0.97,
				BoxCount:       6,
			},
			want: false,
		},
		{
			name: "empty page (no boxes) is not gibberish",
			po:   PageOCR{Text: "", MeanConfidence: 0, BoxCount: 0},
			want: false,
		},
		{
			name: "low mean confidence trips",
			po: PageOCR{
				Text:           "handwritten scrawl that paddle was unsure about across the whole page here",
				MeanConfidence: 0.40,
				BoxCount:       5,
			},
			want: true,
		},
		{
			name: "confident symbol soup trips on lexical test",
			po: PageOCR{
				Text:           "@#$ %^&* ||| ~~~ <<< >>> }{} === +++ ;;; ::: ### *** \\\\\\ ^^^",
				MeanConfidence: 0.95,
				BoxCount:       8,
			},
			want: true,
		},
		{
			name: "consonant runs with no vowels trip",
			po: PageOCR{
				Text:           "brqwx ttttt zxcvb knmpq wrtgh sdfgh jklmn bcdfg pqrst vwxyz",
				MeanConfidence: 0.92,
				BoxCount:       10,
			},
			want: true,
		},
		{
			name: "junk runes trip regardless of confidence",
			po: PageOCR{
				Text:           "ï¿½ï¿½ï¿½ ��� text �� here � page",
				MeanConfidence: 0.99,
				BoxCount:       4,
			},
			want: true,
		},
		{
			name: "short page judged on confidence only (good)",
			po: PageOCR{
				Text:           "Page 12",
				MeanConfidence: 0.96,
				BoxCount:       1,
			},
			want: false,
		},
		{
			name: "numbers and short tokens count as wordlike",
			po: PageOCR{
				Text:           "Total 1,234 items at 9.99 each on line 42 of 100 across 3 pages today now",
				MeanConfidence: 0.94,
				BoxCount:       5,
			},
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, reason := GibberishConfig{}.IsGibberish(c.po)
			if got != c.want {
				t.Fatalf("IsGibberish = %v (%q), want %v", got, reason, c.want)
			}
		})
	}
}

func TestWordlike(t *testing.T) {
	wordy := []string{"report", "revenue", "the", "a", "I", "1,234", "9.99", "42", "co-op", "don't"}
	junk := []string{"@#$", "|||", "brqwx", "zxcvb", "***", "~~~", "}{}"}
	for _, w := range wordy {
		if !wordlike(w) {
			t.Errorf("wordlike(%q) = false, want true", w)
		}
	}
	for _, j := range junk {
		if wordlike(j) {
			t.Errorf("wordlike(%q) = true, want false", j)
		}
	}
}
