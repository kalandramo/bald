// doctor_test.go 钉住 §4.9 doctor 报告的验收面：Opaque 计数与清单、白名单段、
// 被覆盖节点、段状态分组与 build tag 行都必须出现；报告与 Compile 同源
// （同一 InputHash/Plan.Hash），且不引入额外副作用。
package appkit

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"testing"
)

func TestDoctor_ReportsPlanFacts(t *testing.T) {
	cfgPath := writePlanConfig(t, "doctor-app",
		"database:\n  sql:\n    driver: \"sqlite\"\n    source: \"file::memory:?cache=shared&_fk=1\"\n"+
			"script:\n  options:\n    enabled: true\n"+
			"broker:\n  type: \"kafka\"\n")

	winner := Node{Key: "shared", Kind: "script-impl", Phase: PhaseBootstrap, Priority: PriorityLegacy}
	winner.Override, winner.Reason = true, "doctor test override"
	loser := Node{Key: "shared", Kind: "database-impl", Phase: PhaseBootstrap, Priority: PriorityDatabase}
	p, err := Compile(context.Background(), ConfigFile(cfgPath),
		WithUnimplementedSegments("broker"), // broker 段无 Provider 消费者，显式豁免
		fixedNodeProvider([]string{"script"}, winner),
		fixedNodeProvider([]string{"database.sql"}, loser),
	)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	var buf bytes.Buffer
	writeDoctor(&buf, p, []string{"v1", "v2"})
	out := buf.String()

	for _, want := range []string{
		"bald doctor",
		"app: doctor-app ",
		"plan:  " + p.Hash(),
		"input: " + p.InputHash,
		"opaque " + strconv.Itoa(p.OpaqueCount),
		"opaque: logger",
		"whitelisted: broker",
		"override: shared (doctor test override)",
		"build tags: v1,v2",
		"warnings:",
		"bald gen app --check",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("doctor output missing %q:\n%s", want, out)
		}
	}
}

func TestDoctor_BuildTagsLineWhenUnset(t *testing.T) {
	cfgPath := writePlanConfig(t, "doctor-tags", "")
	p, err := Compile(context.Background(), ConfigFile(cfgPath))
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	var buf bytes.Buffer
	writeDoctor(&buf, p, nil)
	if !strings.Contains(buf.String(), "build tags: (none)") {
		t.Fatalf("doctor output missing empty build tag marker:\n%s", buf.String())
	}
}
