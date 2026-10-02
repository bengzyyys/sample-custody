// Package custody 是本地样品流转与保管链。
//
// 一份本地样品数据由 [Store] 表示，通过 [Open] 创建或重新打开；所有
// 登记、分装、交接、销毁与查询都通过 Store 的公开方法完成。数据以 JSON
// 原子落盘，关闭后用同一文件路径重新 [Open] 即可恢复全部样品、保管
// 历史以及交接编号的重复提交判断。
//
// 数量以毫升为单位，只接受大于零、最多三位小数的十进制输入；内部按
// 千分之一毫升的整数精确运算，查询时统一显示三位小数，分装扣减不会
// 因舍入多出或丢失样品。
//
// 典型流程：
//
//	s, err := custody.Open("sample-data.json")
//	// 登记原样
//	s.Register(custody.RegisterInput{ID: "S-001", Qty: "10.000", Holder: "张三", Location: "实验室A"})
//	// 一次分装创建多个子样（任一子样不合法则整次不生效）
//	s.Split(custody.SplitInput{ParentID: "S-001", Parts: []custody.SplitPart{
//		{ID: "S-001-A", Qty: "4.000"},
//		{ID: "S-001-B", Qty: "6.000"},
//	}})
//	// 发起交接，随后由指定接收人在目的地点确认
//	s.Handover(custody.HandoverInput{
//		TransferID: "TR-001", SampleID: "S-001-A",
//		FromHolder: "张三", FromLocation: "实验室A",
//		ToHolder: "李四", ToLocation: "实验室B",
//		HandedOverAt: handoverTime,
//	})
//	s.Confirm(custody.ConfirmInput{TransferID: "TR-001", Receiver: "李四", AtLocation: "实验室B", ReceivedAt: receiveTime})
//	// 销毁该样品当时的全部剩余量（不接受部分销毁）
//	view, _ := s.Destroy(custody.DestroyInput{
//		SampleID: "S-001", Operator: "张三", Location: "实验室A",
//		DestroyedAt: destroyTime, Reason: "实验剩余样品销毁",
//	})
//	// 查询
//	view, _ := s.GetSample("S-001")
//	transfer, _ := s.GetTransfer("TR-001")
package custody

// Ready 表示基线可以运行。
func Ready() bool { return true }
