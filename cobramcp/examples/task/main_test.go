package main

import (
	"testing"

	"github.com/kalandramo/bald/cobramcp/test"
)

func TestTools(t *testing.T) {
	test.Tools(t, taskCmd(), "task")
}
