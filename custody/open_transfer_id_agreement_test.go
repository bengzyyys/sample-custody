package custody

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// 本文件固定“交接集合中的编号（键）与交接记录自身保存的编号（id 字段）
// 必须是同一个非空、不含首尾空白的编号”这条重新打开规则。两处编号不同时，
// 按集合编号查询会命中记录，查询结果却显示记录自身的编号，按该编号反而查
// 不回这条交接，因此整份文件必须打开失败。

// confirmedRecordWithID 把标准已确认交接 rawTR001Confirmed 的自身编号替换成
// 新的 id 字段文本，键仍为 TR-001，用于注入记录自身编号的各种损坏形态。
func confirmedRecordWithID(idField string) string {
	return strings.Replace(rawTR001Confirmed, `"id":"TR-001"`, idField, 1)
}

// TestOpenRejectsTransferKeyIDEscapedSameText 合法 Unicode 转义不得误伤：
// 键把首字符 T 写成合法 Unicode 转义（U+0054）、记录自身编号直接写出，
// 解码后两处都是 TR-001，文件必须正常打开并按 TR-001 查询。
func TestOpenRejectsTransferKeyIDEscapedSameText(t *testing.T) {
	path := writeRawLedger(t, `{
  "version": 1,
  "samples": {`+rawSampleS1TwoHandovers+`},
  "transfers": {
    `+escapedTR001+`: {`+rawTR001Confirmed+`}
  }
}`)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("键经 Unicode 转义、记录 id 直接写出但解码后同一文字应正常打开: %v", err)
	}
	tr, err := s.GetTransfer("TR-001")
	if err != nil {
		t.Fatalf("应按解码后的实际编号 TR-001 查询到交接: %v", err)
	}
	if tr.TransferID != "TR-001" {
		t.Fatalf("查询结果编号应为 TR-001, got %q", tr.TransferID)
	}
}

// TestOpenRejectsTransferRecordIDEscapedSameText 反过来：键直接写出，记录
// 自身编号经合法 Unicode 转义写出同一文字，同样正常恢复。
func TestOpenRejectsTransferRecordIDEscapedSameText(t *testing.T) {
	rec := strings.Replace(rawTR001Confirmed, `"id":"TR-001"`,
		`"id":"`+escapedTR001InText+`"`, 1)
	path := writeRawLedger(t, `{
  "version": 1,
  "samples": {`+rawSampleS1TwoHandovers+`},
  "transfers": {
    "TR-001": {`+rec+`}
  }
}`)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("记录 id 经 Unicode 转义写出同一文字应正常打开: %v", err)
	}
	tr, err := s.GetTransfer("TR-001")
	if err != nil {
		t.Fatal(err)
	}
	if tr.TransferID != "TR-001" {
		t.Fatalf("查询结果编号应为解码后的 TR-001, got %q", tr.TransferID)
	}
}

// TestOpenRejectsConfirmedTransferRecordIDDefects 任务描述的核心损坏及其
// 各种缺失形态（已确认交接）：键一律是 TR-001，记录自身编号分别缺失、
// 为空、只有空白、带首尾空白或写成另一个非空编号，全部必须拒绝。
func TestOpenRejectsConfirmedTransferRecordIDDefects(t *testing.T) {
	cases := []struct {
		name    string
		idField string
		mention []string
	}{
		{
			name:    "记录缺少自身编号",
			idField: "", // 整个 "id":... 字段从记录中消失
			mention: []string{"TR-001", "缺少自身保存的编号"},
		},
		{
			name:    "记录自身编号为空",
			idField: `"id":""`,
			mention: []string{"TR-001", "为空字符串"},
		},
		{
			name:    "记录自身编号只有空白",
			idField: `"id":"   "`,
			mention: []string{"TR-001", "只有空白"},
		},
		{
			name:    "记录自身编号带首尾空白",
			idField: `"id":" TR-001 "`,
			mention: []string{"TR-001", " TR-001 ", "含有首尾空白"},
		},
		{
			name:    "两处非空编号文字不同",
			idField: `"id":"TR-002"`,
			mention: []string{"TR-001", "TR-002", "不一致"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := confirmedRecordWithID(tc.idField)
			if tc.idField == "" {
				// 删除字段时连同末尾逗号一起去掉，保持 JSON 合法。
				rec = strings.Replace(rawTR001Confirmed, `"id":"TR-001",`, "", 1)
			}
			path := writeRawLedger(t, `{
  "version": 1,
  "samples": {`+rawSampleS1TwoHandovers+`},
  "transfers": {
    "TR-001": {`+rec+`}
  }
}`)
			mustRejectOpen(t, path, tc.mention...)
		})
	}
}

// TestOpenRejectsTransferCollectionKeyDefects 集合中的编号本身为空、只有
// 空白或带首尾空白时同样不符合“去首尾空白后保存”的约定，即使记录自身
// 编号正常也不能恢复。
func TestOpenRejectsTransferCollectionKeyDefects(t *testing.T) {
	cases := []struct {
		name    string
		key     string // 含引号的 JSON 键字面文本
		mention []string
	}{
		{
			name:    "集合编号为空字符串",
			key:     `""`,
			mention: []string{"TR-001", "为空字符串"},
		},
		{
			name:    "集合编号只有空白",
			key:     `"   "`,
			mention: []string{"只有空白", "TR-001"},
		},
		{
			name:    "集合编号带首尾空白",
			key:     `" TR-001 "`,
			mention: []string{" TR-001 ", "含有首尾空白", "TR-001"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeRawLedger(t, `{
  "version": 1,
  "samples": {`+rawSampleS1+`},
  "transfers": {
    `+tc.key+`: {`+rawConfirmedTRForS1+`}
  }
}`)
			mustRejectOpen(t, path, tc.mention...)
		})
	}
}

// rawConfirmedTRForS1 是一条关联 rawSampleS1（初始/剩余均为 1.000 毫升）的
// 合法已确认交接，自身编号 TR-001，供集合键损坏用例把损坏集中在键上。
const rawConfirmedTRForS1 = `"id":"TR-001","sampleId":"S1","fromHolder":"张三","fromLocation":"A","toHolder":"李四","toLocation":"B","qty":1000,"handedOverAt":"2026-10-02T10:00:00Z","confirmed":true,"receivedAt":"2026-10-02T11:00:00Z","confirmedBy":"李四"`

// TestOpenRejectsPendingTransferIDMismatch 规则同样适用于待确认交接：
// 样品 S-001 的 pendingId 与集合键都是 TR-002，记录自身却写着 TR-003，
// 待确认内容全部一致也必须拒绝，错误同时指出集合编号与记录自身编号。
func TestOpenRejectsPendingTransferIDMismatch(t *testing.T) {
	wrong := strings.Replace(consistentPendingTR, `"id":"TR-002"`, `"id":"TR-003"`, 1)
	path := writeDivergedScenario(t, "", "", wrong)
	mustRejectOpen(t, path, "TR-002", "TR-003", "不一致")
}

// TestOpenRejectsPendingTransferMissingRecordID 待确认交接缺少自身编号时
// 按编号缺失拒绝，不能用集合键或样品 pendingId 替它补填。
func TestOpenRejectsPendingTransferMissingRecordID(t *testing.T) {
	missing := strings.Replace(consistentPendingTR, `"id":"TR-002",`, "", 1)
	path := writeDivergedScenario(t, "", "", missing)
	mustRejectOpen(t, path, "TR-002", "缺少自身保存的编号")
}

// TestOpenIDMismatchFailsWholeFile 即使同一份文件里另有完全正常的样品与
// 交接，只要一条交接两处编号矛盾，整份数据都不能只加载正常部分：nil
// Store、ErrInvalid、原文件字节不变（mustRejectOpen 已断言）。
func TestOpenIDMismatchFailsWholeFile(t *testing.T) {
	wrong := strings.Replace(consistentPendingTR, `"id":"TR-002"`, `"id":"TR-003"`, 1)
	content := strings.NewReplacer(
		"__REMAINING__", "6750",
		"__SAMPLE_EXTRA__", "",
		"__PENDING_TR__", wrong,
	).Replace(divergedScenario)
	// 在 JSON 顶层另加一条完全合法、与问题交接无关的已确认交接及其样品。
	content = strings.Replace(content, `"transfers": {`,
		`"transfers": {
    "TR-OK": {"id":"TR-OK","sampleId":"S-OK","fromHolder":"甲","fromLocation":"L1","toHolder":"乙","toLocation":"L2","qty":100,"handedOverAt":"2026-10-05T10:00:00Z","confirmed":true,"receivedAt":"2026-10-05T11:00:00Z","confirmedBy":"乙"},`, 1)
	content = strings.Replace(content, `"samples": {`,
		`"samples": {
    "S-OK": {"id":"S-OK","initial":100,"remaining":100,"holder":"乙","location":"L2","children":[],"history":[{"kind":"register","time":"2026-10-05T09:00:00Z","holder":"甲","location":"L1"}]},`, 1)
	path := writeRawLedger(t, content)
	mustRejectOpen(t, path, "TR-002", "TR-003", "不一致")
}

// TestOpenRejectsMismatchViaUnicodeEscape 两处编号经 Unicode 转义写出不同
// 文字时按解码后的实际文字判定为不一致，错误写出 TR-001 与 TR-002，而不
// 是文件里的转义字面。
func TestOpenRejectsMismatchViaUnicodeEscape(t *testing.T) {
	// 键经转义解码为 TR-001，记录 id 经转义解码为 TR-002：按解码后的实际
	// 文字判定为不一致，错误写出 TR-001 与 TR-002，而不是文件里的转义字面。
	rec := strings.Replace(rawTR001Confirmed, `"id":"TR-001"`,
		`"id":"`+"\\u0054"+`R-002"`, 1)
	path := writeRawLedger(t, `{
  "version": 1,
  "samples": {`+rawSampleS1TwoHandovers+`},
  "transfers": {
    `+escapedTR001+`: {`+rec+`}
  }
}`)
	mustRejectOpen(t, path, "TR-001", "TR-002", "不一致")
}

// TestOpenTransferSampleIDAndHistoryTextNotPartOfIDCheck 交接中的样品编号、
// 保管历史说明里提到的交接编号只是关联或说明，不参与两处交接编号的一致性
// 判断：键与记录 id 都是 TR-001 时，sampleId 写成形如交接编号的 TR-002、
// 历史说明提到不存在的 TR-009，都不应报“编号不一致”。sampleId 关联不到
// 样品时按既有的“关联样品不存在”规则报错，证明它被当作关联字段处理。
func TestOpenTransferSampleIDAndHistoryTextNotPartOfIDCheck(t *testing.T) {
	// 只在说明文字里提到 TR-009：文件正常打开。
	sample := `"S1": {"id":"S1","initial":1000,"remaining":1000,"holder":"张三","location":"A","children":[],"history":[{"kind":"register","time":"2026-10-02T09:00:00Z","holder":"张三","location":"A","detail":"说明文字提到交接 TR-009 与 TR-001"}]}`
	okPath := writeRawLedger(t, `{
  "version": 1,
  "samples": {`+sample+`},
  "transfers": {
    "TR-001": {`+rawConfirmedTRForS1+`}
  }
}`)
	s, err := Open(okPath)
	if err != nil {
		t.Fatalf("说明文字提到其他交接编号不应影响两处编号一致性: %v", err)
	}
	if tr, err := s.GetTransfer("TR-001"); err != nil || tr.TransferID != "TR-001" {
		t.Fatalf("TR-001 应正常恢复: %v %+v", err, tr)
	}

	// sampleId 写成 TR-002（看似交接编号）：按关联样品不存在处理，而不是
	// 按两处交接编号不一致处理。
	rec := strings.Replace(rawConfirmedTRForS1, `"sampleId":"S1"`, `"sampleId":"TR-002"`, 1)
	badPath := writeRawLedger(t, `{
  "version": 1,
  "samples": {`+sample+`},
  "transfers": {
    "TR-001": {`+rec+`}
  }
}`)
	st, err := Open(badPath)
	if err == nil {
		t.Fatalf("关联样品不存在时仍应拒绝打开")
	}
	if st != nil {
		t.Fatalf("打开失败不应返回 Store")
	}
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("应包装 ErrInvalid, got %v", err)
	}
	if !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("应按关联样品不存在报错, got %v", err)
	}
	if strings.Contains(err.Error(), "不一致") {
		t.Fatalf("样品编号是关联字段，不应按两处交接编号不一致报错: %v", err)
	}
}

// TestOpenConsistentTransferIDsStayConsistentAfterRestore 合法记录恢复后，
// 按交接编号查询、样品待确认详情以及相同交出请求返回的交接编号必须保持
// 一致；待确认交接仍由原指定接收人按原条件确认。
func TestOpenConsistentTransferIDsStayConsistentAfterRestore(t *testing.T) {
	s, hAt1, rAt1, hAt2, rAt2 := setupDivergedHandover(t)

	s2, err := Open(s.path)
	if err != nil {
		t.Fatalf("公开操作落盘的合法文件重新打开不应失败: %v", err)
	}
	tr, err := s2.GetTransfer("TR-002")
	if err != nil {
		t.Fatal(err)
	}
	if tr.TransferID != "TR-002" {
		t.Fatalf("按 TR-002 查询应返回编号 TR-002, got %q", tr.TransferID)
	}
	p, err := s2.GetSample("S-001")
	if err != nil {
		t.Fatal(err)
	}
	if p.PendingTransfer == nil || p.PendingTransfer.TransferID != "TR-002" {
		t.Fatalf("样品待确认详情应显示同一编号 TR-002: %+v", p.PendingTransfer)
	}
	// 相同交出请求幂等返回，编号仍是 TR-002 且不新增记录。
	again, err := s2.Handover(HandoverInput{
		TransferID: "TR-002", SampleID: "S-001",
		FromHolder: "李四", FromLocation: "实验室B",
		ToHolder: "王五", ToLocation: "实验室C", HandedOverAt: hAt2,
	})
	if err != nil {
		t.Fatalf("相同交出请求应幂等返回原交接: %v", err)
	}
	if again.TransferID != "TR-002" || len(s2.data.Transfers) != 2 {
		t.Fatalf("幂等返回必须保持编号且不新增记录: %+v", again)
	}
	// 待确认交接仍由原指定接收人按原条件确认。
	done, err := s2.Confirm(ConfirmInput{"TR-002", "王五", "实验室C", rAt2})
	if err != nil {
		t.Fatalf("恢复后仍应由原指定接收人确认: %v", err)
	}
	if !done.Confirmed || done.TransferID != "TR-002" {
		t.Fatalf("确认结果编号应保持 TR-002: %+v", done)
	}

	// 已确认的 TR-001 编号与内容仍是当时的历史事实。
	old, err := s2.GetTransfer("TR-001")
	if err != nil {
		t.Fatal(err)
	}
	requireOldFirstTransfer(t, old, hAt1, rAt1)
}

// TestOpenConfirmedTransferKeptAfterSplitHolderChangeAndDestroy 已确认交接
// 在样品后来分装、换持有人/地点并销毁后，仍按原有规则保留历史事实：编号
// 两处一致、内容不被样品当前信息改写，重新打开可查，相同交出请求仍返回
// 原记录。
func TestOpenConfirmedTransferKeptAfterSplitHolderChangeAndDestroy(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/data.json"
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// 固定时钟：登记/分装历史时间必须早于显式给出的交接与销毁时间，否则
	// 销毁时间核对会把真实当前时刻当作更晚的历史。
	hAt := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return hAt.Add(-time.Hour) }
	mustRegister(t, s, "S-001", "10.000", "张三", "实验室A")
	if _, err := s.Handover(HandoverInput{
		TransferID: "TR-001", SampleID: "S-001",
		FromHolder: "张三", FromLocation: "实验室A",
		ToHolder: "李四", ToLocation: "实验室B", HandedOverAt: hAt,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Confirm(ConfirmInput{"TR-001", "李四", "实验室B", hAt.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Split(SplitInput{ParentID: "S-001", Parts: []SplitPart{{ID: "S-001-A", Qty: "4.000"}}}); err != nil {
		t.Fatal(err)
	}
	// 父样剩余 6.000 + 直接分出 4.000 恰好守恒，可以销毁；销毁后持有人、
	// 地点保留为李四/实验室B，与 TR-001 的历史事实并存。
	if _, err := s.Destroy(DestroyInput{
		SampleID: "S-001", Operator: "李四", Location: "实验室B",
		At: hAt.Add(2 * time.Hour), Reason: "实验结束按规程销毁",
	}); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("样品分装、换人并销毁后，两处编号一致的已确认交接应随文件恢复: %v", err)
	}
	tr, err := s2.GetTransfer("TR-001")
	if err != nil {
		t.Fatal(err)
	}
	if !tr.Confirmed || tr.TransferID != "TR-001" || tr.Qty != "10.000" ||
		tr.FromHolder != "张三" || tr.ToHolder != "李四" {
		t.Fatalf("已确认交接必须保留当时的历史事实，不被现状改写: %+v", tr)
	}
	// 按显示编号 TR-001 必须能查回同一条记录（视图每次重新构建、时间指针
	// 地址也不同，因此逐字段并按实际时刻比较）。
	byKey, err := s2.GetTransfer(tr.TransferID)
	if err != nil {
		t.Fatalf("按查询结果编号 %q 应查回同一条交接: %v", tr.TransferID, err)
	}
	same := byKey.TransferID == tr.TransferID && byKey.SampleID == tr.SampleID &&
		byKey.FromHolder == tr.FromHolder && byKey.FromLocation == tr.FromLocation &&
		byKey.ToHolder == tr.ToHolder && byKey.ToLocation == tr.ToLocation &&
		byKey.Qty == tr.Qty && byKey.HandedOverAt.Equal(tr.HandedOverAt) &&
		byKey.Confirmed == tr.Confirmed && byKey.ConfirmedBy == tr.ConfirmedBy &&
		byKey.ConfirmedAt == tr.ConfirmedAt &&
		(byKey.ReceivedAt == nil) == (tr.ReceivedAt == nil) &&
		(byKey.ReceivedAt == nil || byKey.ReceivedAt.Equal(*tr.ReceivedAt))
	if !same {
		t.Fatalf("按 %q 查回的交接与原视图不一致: %+v vs %+v", tr.TransferID, byKey, tr)
	}
	// 相同交出请求即使样品已销毁仍幂等返回原记录。
	again, err := s2.Handover(HandoverInput{
		TransferID: "TR-001", SampleID: "S-001",
		FromHolder: "张三", FromLocation: "实验室A",
		ToHolder: "李四", ToLocation: "实验室B", HandedOverAt: hAt,
	})
	if err != nil {
		t.Fatalf("已确认交接的相同交出请求应继续返回原记录: %v", err)
	}
	if again.TransferID != "TR-001" || !again.Confirmed || again.Qty != "10.000" {
		t.Fatalf("幂等返回应保持原编号与原内容: %+v", again)
	}
}

// TestOpenTransferIDAgreementErrorIsInvalid 固定错误分类：两处编号不一致
// 必须能被 errors.Is 判定为 ErrInvalid，且不是冲突或未找到类。
func TestOpenTransferIDAgreementErrorIsInvalid(t *testing.T) {
	rec := confirmedRecordWithID(`"id":"TR-002"`)
	path := writeRawLedger(t, `{
  "version": 1,
  "samples": {`+rawSampleS1TwoHandovers+`},
  "transfers": {
    "TR-001": {`+rec+`}
  }
}`)
	s, err := Open(path)
	if s != nil {
		t.Fatalf("打开失败必须返回 nil Store")
	}
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("两处交接编号不一致应判定为 ErrInvalid, got %v", err)
	}
	if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) {
		t.Fatalf("不应归类为 ErrConflict 或 ErrNotFound: %v", err)
	}
}
