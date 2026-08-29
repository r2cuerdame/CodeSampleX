package web

import (
	"strings"
	"testing"
)

// The first thing a reader does on a package page is ask a different question
// — a different release, a different API, a different machine. The controls
// that let them do it were folded away BELOW the answer, so the answer to the
// question they did not ask came first and the way to ask theirs came second.
//
// R2C-68 settles the order: 조건 변경 above 현재 조건. The fold is one summary
// line tall when closed, so the answer stays in view directly beneath it.
//
// The deep-link anchor is the constraint this must not break. #cube is what
// every shared link and every filter reload lands on, and the answer has to
// stay inside it — that is why the answer was put here rather than above the
// section in the first place.
func TestTheWayToAskADifferentQuestionComesBeforeTheAnswer(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = uuidNavStore() })
	body := get(t, mux, uuidLeaf+"&lang=ko").Body.String()

	change := strings.Index(body, `<details id="cubechange"`)
	answer := strings.Index(body, `class="answer `)
	if change < 0 {
		t.Fatal("the package page has no way to change the conditions")
	}
	if answer < 0 {
		t.Fatal("the package page has no answer card")
	}
	if change > answer {
		t.Errorf("현재 조건 is rendered before 조건 변경 (answer at %d, controls at %d); "+
			"the reader is shown the answer to a question they have not asked yet", answer, change)
	}

	// Both still live inside #cube, because that anchor is where a shared
	// link lands. Moving either one above the section is what would make a
	// deep link scroll past the thing it was made to show.
	section := cubeSection(t, body)
	if !strings.Contains(section, `<details id="cubechange"`) {
		t.Error("the controls left #cube, so a deep link no longer lands on them")
	}
	if !strings.Contains(section, `class="answer `) {
		t.Error("the answer left #cube, so a deep link scrolls past it")
	}
}
