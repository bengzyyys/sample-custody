package custody

import (
	"errors"
	"strings"
	"testing"
)

// 交接集合中同一编号写两条时，打开必须整份失败：返回 nil Store、错误可用
// errors.Is 判定为 ErrInvalid、写明重复编号，且原文件内容不被改写。

// 一条合法的已确认交接与一条合法的待确认交接（关联不同样品），供拼接用。
const (
	rawSampleA = `"S1": {"id":"S1","initial":1000,"remaining":1000,"holder":"张三","location":"A","children":[],"history":[{"kind":"register","time":"2026-10-02T09:00:00Z","holder":"张三","location":"A"}]}`
	rawSampleB = `"S2": {"id":"S2","initial":500,"remaining":500,"holder":"王五","location":"C","children":[],"history":[{"kind":"register","time":"2026-10-02T09:00:00Z","holder":"王五","location":"C"}]}`

	rawTransferConfirmed = `{"id":"TR-001","sampleId":"S1","fromHolder":"张三","fromLocation":"A","toHolder":"李四","toLocation":"B","qty":1000,"handedOverAt":"2026-10-02T10:00:00Z","confirmed":true,"receivedAt":"2026-10-02T11:00:00Z","confirmedBy":"李四"}`
	rawTransferPending   = `{"id":"TR-002","sampleId":"S2","fromHolder":"王五","fromLocation":"C","toHolder":"赵六","toLocation":"D","qty":500,"handedOverAt":"2026-10-02T12:00:00Z","confirmed":false}`
)

func TestOpenRejectsDuplicateTransferIDAdjacent(t *testing.T) {
	// 相邻的两条 TR-001，内容不同：后一条不得悄悄覆盖前一条。
	path := writeRawLedger(t, `{
		"version": 1,
		"samples": {`+rawSampleA+`},
		"transfers": {
			"TR-001": `+rawTransferConfirmed+`,
			"TR-001": {"id":"TR-001","sampleId":"S1","fromHolder":"张三","fromLocation":"A","toHolder":"王五","toLocation":"D","qty":1000,"handedOverAt":"2026-10-02T12:00:00Z","confirmed":false}
		}
	}`)
	mustRejectOpen(t, path, "TR-001", "重复")
}

func TestOpenRejectsDuplicateTransferIDNonAdjacent(t *testing.T) {
	// 隔着其他交接的重复编号同样拒绝。
	path := writeRawLedger(t, `{
		"version": 1,
		"samples": {`+rawSampleA+`,`+rawSampleB+`},
		"transfers": {
			"TR-001": `+rawTransferConfirmed+`,
			"TR-002": `+rawTransferPending+`,
			"TR-001": {"id":"TR-001","sampleId":"S1","fromHolder":"张三","fromLocation":"A","toHolder":"李四","toLocation":"B","qty":1000,"handedOverAt":"2026-10-02T10:00:00Z","confirmed":false}
		}
	}`)
	mustRejectOpen(t, path, "TR-001", "重复")
}

func TestOpenRejectsDuplicateTransferIDIdenticalContent(t *testing.T) {
	// 两条记录内容完全相同也属于重复，不能挑其中一条接受。
	path := writeRawLedger(t, `{
		"version": 1,
		"samples": {`+rawSampleA+`},
		"transfers": {
			"TR-001": `+rawTransferConfirmed+`,
			"TR-001": `+rawTransferConfirmed+`
		}
	}`)
	mustRejectOpen(t, path, "TR-001", "重复")
}

func TestOpenRejectsDuplicateTransferIDUnicodeEscape(t *testing.T) {
	// 编号按 JSON 解码后实际表示的文字判断："TR-001" 与把 0 写成
	// Unicode 转义的 "TR-001" 是同一个编号，仍属重复。
	escaped := strings.Replace(rawTransferPending, `"id":"TR-002"`, `"id":"TR-001"`, 1)
	path := writeRawLedger(t, `{
		"version": 1,
		"samples": {`+rawSampleA+`,`+rawSampleB+`},
		"transfers": {
			"TR-001": `+rawTransferConfirmed+`,
			"TR-001": `+escaped+`
		}
	}`)
	// 上面两个字面键相同，先确认普通重复被拒绝；再验证转义写法。
	mustRejectOpen(t, path, "TR-001", "重复")

	path = writeRawLedger(t, `{
		"version": 1,
		"samples": {`+rawSampleA+`,`+rawSampleB+`},
		"transfers": {
			"TR-001": `+rawTransferConfirmed+`,
			"TR-\u003001": `+escaped+`
		}
	}`)
	mustRejectOpen(t, path, "TR-001", "重复")
}

func TestOpenAllowsTransferIDMentionedInText(t *testing.T) {
	// 保管历史说明、交接字段值等说明文字中提到 TR-001 不算重复。
	path := writeRawLedger(t, `{
		"version": 1,
		"samples": {
			"S1": {"id":"S1","initial":1000,"remaining":1000,"holder":"李四","location":"B","children":[],"history":[
				{"kind":"register","time":"2026-10-02T09:00:00Z","holder":"张三","location":"A"},
				{"kind":"transfer-out","time":"2026-10-02T10:00:00Z","holder":"张三","location":"A","detail":"发起交接 TR-001，待 李四 在 B 接收"},
				{"kind":"transfer-in","time":"2026-10-02T11:00:00Z","holder":"李四","location":"B","detail":"交接 TR-001 确认接收（交出时间 2026-10-02T10:00:00Z）"}
			]},
			"S2": {"id":"S2","initial":500,"remaining":500,"holder":"王五","location":"C","children":[],"history":[{"kind":"register","time":"2026-10-02T09:00:00Z","holder":"王五","location":"C"}],"pendingId":"TR-002"}
		},
		"transfers": {
			"TR-001": `+rawTransferConfirmed+`,
			"TR-002": {"id":"TR-002","sampleId":"S2","fromHolder":"王五","fromLocation":"C","toHolder":"赵六","toLocation":"D","qty":500,"handedOverAt":"2026-10-02T12:00:00Z","confirmed":false}
		}
	}`)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("说明文字中提到交接编号不应误报重复: %v", err)
	}
	if _, err := s.GetTransfer("TR-001"); err != nil {
		t.Fatalf("合法交接应可查询: %v", err)
	}
}

func TestOpenAllowsMultipleTransfersForSameSample(t *testing.T) {
	// 同一样品的多次转手是不同编号的交接，不能误认为编号重复。
	path := writeRawLedger(t, `{
		"version": 1,
		"samples": {
			"S1": {"id":"S1","initial":1000,"remaining":1000,"holder":"王五","location":"C","children":[],"history":[
				{"kind":"register","time":"2026-10-02T09:00:00Z","holder":"张三","location":"A"},
				{"kind":"transfer-out","time":"2026-10-02T10:00:00Z","holder":"张三","location":"A","detail":"发起交接 TR-001，待 李四 在 B 接收"},
				{"kind":"transfer-in","time":"2026-10-02T11:00:00Z","holder":"李四","location":"B","detail":"交接 TR-001 确认接收（交出时间 2026-10-02T10:00:00Z）"},
				{"kind":"transfer-out","time":"2026-10-02T12:00:00Z","holder":"李四","location":"B","detail":"发起交接 TR-002，待 王五 在 C 接收"},
				{"kind":"transfer-in","time":"2026-10-02T13:00:00Z","holder":"王五","location":"C","detail":"交接 TR-002 确认接收（交出时间 2026-10-02T12:00:00Z）"}
			]}
		},
		"transfers": {
			"TR-001": `+rawTransferConfirmed+`,
			"TR-002": {"id":"TR-002","sampleId":"S1","fromHolder":"李四","fromLocation":"B","toHolder":"王五","toLocation":"C","qty":1000,"handedOverAt":"2026-10-02T12:00:00Z","confirmed":true,"receivedAt":"2026-10-02T13:00:00Z","confirmedBy":"王五"}
		}
	}`)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("同一样品的多次转手不应误认为编号重复: %v", err)
	}
	for _, id := range []string{"TR-001", "TR-002"} {
		tr, err := s.GetTransfer(id)
		if err != nil {
			t.Fatalf("交接 %s 应可查询: %v", id, err)
		}
		if !tr.Confirmed {
			t.Fatalf("交接 %s 应保持已确认: %+v", id, tr)
		}
	}
}

func TestOpenRejectsDuplicateTransferIDKeepsValidEntriesUnloaded(t *testing.T) {
	// 文件内另有正常样品和交接时也不能只加载正常部分。
	path := writeRawLedger(t, `{
		"version": 1,
		"samples": {`+rawSampleA+`,`+rawSampleB+`},
		"transfers": {
			"TR-002": `+rawTransferPending+`,
			"TR-001": `+rawTransferConfirmed+`,
			"TR-001": `+rawTransferConfirmed+`
		}
	}`)
	mustRejectOpen(t, path, "TR-001", "重复")
	if _, err := Open(path); !errors.Is(err, ErrInvalid) {
		t.Fatalf("重复编号应判定为 ErrInvalid, got %v", err)
	}
}
