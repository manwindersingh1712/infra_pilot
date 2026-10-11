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

func TestLayoutTerminatesOnCyclesAndIgnoresUnknownNodes(t *testing.T) {
	got := layout([]string{"a", "b"}, [][2]string{{"a", "b"}, {"b", "a"}, {"a", "zzz"}, {"a", "a"}})
	if len(got) != 2 {
		t.Fatalf("got %v", got)
	}
}

func TestLayoutEmptyIsNotNil(t *testing.T) {
	if got := layout(nil, nil); got == nil || len(got) != 0 {
		t.Fatalf("got %#v, want empty non-nil slice", got)
	}
}
