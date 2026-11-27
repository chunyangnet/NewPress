package articlewidgets

import (
	"fmt"
	"strings"
	"testing"
)

func TestRatingUsesAccessibleContinuousStars(t *testing.T) {
	for _, max := range []int{5, 10} {
		t.Run(fmt.Sprint(max), func(t *testing.T) {
			c := example("rating")
			c.Max = float64(max)
			html := Render(c, 1, "chunnel")
			if got := strings.Count(html, `class="npw-star-shape"`); got != max {
				t.Fatalf("got %d stars, want %d", got, max)
			}
			for i := 1; i <= max; i++ {
				if !strings.Contains(html, fmt.Sprintf(`aria-label="%d 分" aria-pressed="false"`, i)) {
					t.Fatalf("missing accessible score %d", i)
				}
			}
			if strings.Contains(html, "☆") {
				t.Fatal("rating must not depend on a font glyph")
			}
		})
	}
}
