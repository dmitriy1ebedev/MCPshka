package toolstats

import (
	"context"
	"testing"
	"time"
)

func TestHTTPReportSeparatesServerFromRequest(t *testing.T) {
	s := New(time.Now())
	s.RecordHTTP("local", 1500*time.Microsecond, 250*time.Microsecond)
	s.RecordHTTP("untrusted route", time.Hour, time.Hour)
	r := s.HTTPReport()
	if len(r) != 3 || r[2].Requests != 1 || r[2].HTTPMS != 1.5 || r[2].ServerMS != 0.25 || r[2].HTTPP95MS != 1.5 || r[2].ServerP95MS != 0.25 {
		t.Fatalf("%+v", r)
	}
}
func TestNestedAndParallelHTTPToolIntervals(t *testing.T) {
	timing := &httpTiming{}
	ctx := context.WithValue(context.Background(), httpTimingKey{}, timing)
	outer := BeginHTTPTool(ctx)
	first := BeginHTTPTool(ctx)
	second := BeginHTTPTool(ctx)
	first()
	if timing.server != 0 {
		t.Fatal("nested call counted early")
	}
	second()
	if timing.active != 1 || timing.server != 0 {
		t.Fatal("nested intervals double counted")
	}
	outer()
	if timing.active != 0 || timing.server <= 0 {
		t.Fatal("missing handler interval")
	}
	BeginHTTPTool(context.Background())()
}

func TestHTTPFinishIncludesActiveTool(t *testing.T) {
	before := Default.HTTPReport()[2]
	ctx, finish := BeginHTTP(context.Background())
	stop := BeginHTTPTool(ctx)
	timing := ctx.Value(httpTimingKey{}).(*httpTiming)
	timing.since = time.Now().Add(-time.Millisecond)
	finish("local")
	stop()
	after := Default.HTTPReport()[2]
	if after.Requests != before.Requests+1 || after.ServerMS-before.ServerMS < 1 {
		t.Fatalf("active handler omitted: before=%+v after=%+v", before, after)
	}
}
