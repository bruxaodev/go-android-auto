package ocr

import (
	"strings"
	"testing"
)

func TestFindTextBoundsFromTSV(t *testing.T) {
	tsv := strings.Join([]string{
		"level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext",
		"5\t1\t1\t1\t1\t1\t100\t200\t40\t20\t96\tCreate",
		"5\t1\t1\t1\t1\t2\t150\t198\t70\t24\t95\taccount",
		"5\t1\t1\t1\t2\t1\t100\t240\t30\t18\t94\tOther",
	}, "\n")

	bounds, err := (&Tesseract{}).findTextBoundsFromTSV(strings.NewReader(tsv), "Create account")
	if err != nil {
		t.Fatalf("findTextBoundsFromTSV returned error: %v", err)
	}

	if bounds == nil {
		t.Fatal("expected bounds, got nil")
	}

	if bounds.Left != 100 || bounds.Top != 198 || bounds.Right != 220 || bounds.Bottom != 222 {
		t.Fatalf("unexpected bounds: %+v", bounds)
	}
}

func TestFindTextBoundsFromTSVReturnsMatchingWordBounds(t *testing.T) {
	tsv := strings.Join([]string{
		"level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext",
		"5\t1\t1\t1\t1\t1\t100\t200\t80\t20\t96\tMonth",
		"5\t1\t1\t1\t1\t2\t260\t198\t40\t24\t95\tDay",
		"5\t1\t1\t1\t1\t3\t380\t198\t50\t24\t95\tYear",
	}, "\n")

	bounds, err := (&Tesseract{}).findTextBoundsFromTSV(strings.NewReader(tsv), "mont")
	if err != nil {
		t.Fatalf("findTextBoundsFromTSV returned error: %v", err)
	}

	if bounds == nil {
		t.Fatal("expected bounds, got nil")
	}

	if bounds.Left != 100 || bounds.Top != 200 || bounds.Right != 180 || bounds.Bottom != 220 {
		t.Fatalf("unexpected bounds: %+v", bounds)
	}
}

func TestFindTextBoundsFromTSVSelectsZeroBasedIndexedRepeatedText(t *testing.T) {
	tsv := strings.Join([]string{
		"level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext",
		"5\t1\t1\t1\t1\t1\t10\t100\t30\t20\t96\tpalavra",
		"5\t1\t1\t1\t1\t2\t60\t100\t30\t20\t96\tpalavra",
		"5\t1\t1\t1\t2\t1\t10\t160\t30\t20\t96\tpalavra",
		"5\t1\t1\t1\t2\t2\t80\t160\t40\t24\t96\tpalavra",
	}, "\n")

	bounds, err := (&Tesseract{}).findTextBoundsFromTSV(strings.NewReader(tsv), `[3]."palavra"`)
	if err != nil {
		t.Fatalf("findTextBoundsFromTSV returned error: %v", err)
	}

	if bounds == nil {
		t.Fatal("expected bounds, got nil")
	}

	if bounds.Left != 80 || bounds.Top != 160 || bounds.Right != 120 || bounds.Bottom != 184 {
		t.Fatalf("unexpected bounds: %+v", bounds)
	}
}

func TestFindTextBoundsFromTSVReturnsErrorForMissingIndexedOccurrence(t *testing.T) {
	tsv := strings.Join([]string{
		"level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext",
		"5\t1\t1\t1\t1\t1\t10\t100\t30\t20\t96\tpalavra",
	}, "\n")

	_, err := (&Tesseract{}).findTextBoundsFromTSV(strings.NewReader(tsv), `[3]."palavra"`)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), `target occurrence [3]."palavra" not found`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFindTextBoundsFromTSVPrefersExactCaseMatches(t *testing.T) {
	tsv := strings.Join([]string{
		"level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext",
		"5\t1\t1\t1\t1\t1\t268\t479\t90\t29\t96\tEnter",
		"5\t1\t1\t1\t1\t2\t369\t486\t77\t30\t96\tyour",
		"5\t1\t1\t1\t1\t3\t459\t477\t144\t39\t96\tbirthday",
		"5\t1\t1\t1\t1\t4\t616\t477\t62\t31\t96\tand",
		"5\t1\t1\t1\t1\t5\t693\t477\t122\t39\t96\tgender",
		"5\t1\t1\t1\t2\t1\t103\t656\t111\t48\t96\tMonth",
		"5\t1\t1\t1\t2\t2\t295\t672\t23\t12\t75\tv",
		"5\t1\t1\t1\t2\t3\t430\t664\t63\t37\t96\tDay",
		"5\t1\t1\t1\t2\t4\t754\t664\t78\t29\t96\tYear",
		"5\t1\t1\t1\t3\t1\t102\t836\t127\t47\t96\tGender",
		"5\t1\t1\t1\t3\t2\t949\t852\t23\t12\t79\tv",
	}, "\n")

	dayBounds, err := (&Tesseract{}).findTextBoundsFromTSV(strings.NewReader(tsv), "Day")
	if err != nil {
		t.Fatalf("findTextBoundsFromTSV Day returned error: %v", err)
	}
	if dayBounds.Left != 430 || dayBounds.Top != 664 || dayBounds.Right != 493 || dayBounds.Bottom != 701 {
		t.Fatalf("unexpected Day bounds: %+v", dayBounds)
	}

	genderBounds, err := (&Tesseract{}).findTextBoundsFromTSV(strings.NewReader(tsv), "Gender")
	if err != nil {
		t.Fatalf("findTextBoundsFromTSV Gender returned error: %v", err)
	}
	if genderBounds.Left != 102 || genderBounds.Top != 836 || genderBounds.Right != 229 || genderBounds.Bottom != 883 {
		t.Fatalf("unexpected Gender bounds: %+v", genderBounds)
	}
}

func TestWordsFromTSV(t *testing.T) {
	tsv := strings.Join([]string{
		"level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext",
		"5\t1\t1\t1\t1\t1\t100\t200\t40\t20\t96\tBG",
		"5\t1\t1\t1\t1\t2\t150\t198\t70\t24\t95\t01",
	}, "\n")

	words, err := wordsFromTSV(strings.NewReader(tsv))
	if err != nil {
		t.Fatalf("wordsFromTSV returned error: %v", err)
	}

	if len(words) != 2 {
		t.Fatalf("expected 2 words, got %d", len(words))
	}
	if words[1].Text != "01" || words[1].Bounds.Left != 150 || words[1].Bounds.Right != 220 {
		t.Fatalf("unexpected word: %+v", words[1])
	}
}
