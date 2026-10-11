package api

import (
	"reflect"
	"testing"
)

func TestLayoutChainGoesLeftToRight(t *testing.T) {
	got := layout([]string{"a", "b", "c"}, [][2]string{{"a", "b"}, {"b", "c"}})
	want := []position{{"a", 0, 0}, {"b", 380, 0}, {"c", 760, 0}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestLayoutStacksNodesInARank(t *testing.T) {
	got := layout([]string{"a", "b", "c"}, [][2]string{{"a", "c"}})
	want := []position{{"a", 0, 0}, {"b", 0, 230}, {"c", 380, 0}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestLayoutDiamondUsesLongestPath(t *testing.T) {
	got := layout([]string{"a", "b", "c", "d"}, [][2]string{{"a", "b"}, {"a", "c"}, {"b", "d"}, {"c", "d"}})
	want := []position{{"a", 0, 0}, {"b", 380, 0}, {"c", 380, 230}, {"d", 760, 0}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

// The skip edge a->c must not pull c back to rank 1: the longest path wins.
func TestLayoutSkipEdgeKeepsLongestPath(t *testing.T) {
	want := []position{{"a", 0, 0}, {"b", 380, 0}, {"c", 760, 0}}
	for name, edges := range map[string][][2]string{
		"skip last":  {{"a", "b"}, {"b", "c"}, {"a", "c"}},
		"skip first": {{"a", "c"}, {"a", "b"}, {"b", "c"}},
	} {
		if got := layout([]string{"a", "b", "c"}, edges); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: got %v", name, got)
		}
	}
}

// Ranks stay below len(ids), so a cycle settles: b is one rank after a, and
// the back edge cannot push a further right.
func TestLayoutCycleHasExactPositions(t *testing.T) {
	got := layout([]string{"a", "b"}, [][2]string{{"a", "b"}, {"b", "a"}})
	want := []position{{"a", 0, 0}, {"b", 380, 0}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestLayoutIgnoresSelfLoops(t *testing.T) {
	got := layout([]string{"a", "b", "c"}, [][2]string{{"a", "b"}, {"b", "b"}})
	want := []position{{"a", 0, 0}, {"b", 380, 0}, {"c", 0, 230}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

// a->zzz->b would put b at rank 2 if the unknown node counted.
func TestLayoutIgnoresEdgesWithUnknownEnds(t *testing.T) {
	got := layout([]string{"a", "b", "c"}, [][2]string{{"a", "zzz"}, {"zzz", "b"}, {"a", "b"}, {"qqq", "c"}})
	want := []position{{"a", 0, 0}, {"b", 380, 0}, {"c", 0, 230}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestLayoutEmptyIsNotNil(t *testing.T) {
	if got := layout(nil, nil); got == nil || len(got) != 0 {
		t.Fatalf("got %#v, want empty non-nil slice", got)
	}
}
