package main

import (
	"slices"
	"testing"
)

// glfwAttrs is what the GLFW in raylib asks for with FlagMsaa4xHint.
var glfwAttrs = []uint32{nsglAccelerated, 74, 96, 101, 99, 0x3200, 8, 24, 11, 8, 12, 24, 13, 8, 5, nsglSampleBuffers, 1, nsglSamples, 4, 0}

func TestReadAttrs(t *testing.T) {
	if got := readAttrs(&glfwAttrs[0]); !slices.Equal(got, glfwAttrs) {
		t.Errorf("readAttrs = %v, want %v", got, glfwAttrs)
	}
}

func TestWithoutAttrs(t *testing.T) {
	got := withoutAttrs(glfwAttrs, []uint32{nsglAccelerated})
	if want := glfwAttrs[1:]; !slices.Equal(got, want) {
		t.Errorf("without accelerated = %v, want %v", got, want)
	}
	got = withoutAttrs(glfwAttrs, []uint32{nsglAccelerated, nsglSampleBuffers, nsglSamples})
	if want := append(slices.Clone(glfwAttrs[1:15]), 0); !slices.Equal(got, want) {
		t.Errorf("without accelerated and samples = %v, want %v", got, want)
	}
	// A value equal to a dropped attribute stays: 73 here is a color size.
	attrs := []uint32{8, nsglAccelerated, 0}
	if got := withoutAttrs(attrs, []uint32{nsglAccelerated}); !slices.Equal(got, attrs) {
		t.Errorf("value 73 = %v, want %v", got, attrs)
	}
}
