# 本地样品流转与保管链

这是一个在本机运行的本地样品流转与保管链。样品数据保存在一个本地 JSON 文件中，
关闭后重新打开，记录与交接编号的重复提交判断仍然有效。

## 使用

```bash
go test ./...
```

测试通过表示基线包可以加载。

## 公开入口

通过 `custody.Store` 的公开方法完成操作与查询：

```go
// 创建或打开本地数据
store, _ := custody.Create("/path/to/custody.json")   // 文件已存在时拒绝
store, _ := custody.Open("/path/to/custody.json")     // 文件不存在时拒绝
defer store.Close()

// 登记原样：编号在数据内唯一；编号、持有人、地点去首尾空白后不能为空；
// 数量以毫升表示，只接受大于零、最多三位小数的十进制值。
sample, _ := store.Register("S1", "10", "Alice", "Lab A")

// 分装：一次可创建多个子样，各子样有独立编号，总量不得超过来源剩余量；
// 任一编号重复或数量不合法，整次分装都不生效。子样继承当时的持有人和地点，
// 并可继续分装。
sample, _ = store.Aliquot("S1", []custody.Aliquot{
    {SampleID: "S1-1", Quantity: "3.5"},
    {SampleID: "S1-2", Quantity: "1.25"},
})

// 发起交接：交出人和交出地点必须与样品当前记录一致，接收人不能与交出人相同；
// 交接转移该样品全部剩余量，发起后处于待确认，原持有人和地点保持不变。
now := time.Now()
handover, _ := store.InitiateHandover(custody.HandoverRequest{
    HandoverID: "H1", SampleID: "S1",
    Giver: "Alice", GiverLocation: "Lab A",
    Receiver: "Bob", Destination: "Lab B",
    HandoverTime: now,
})

// 确认接收：只有指定接收人能在目的地点确认，接收时间不能早于交出时间；
// 确认后才改变持有人和地点，并结束待确认状态。
handover, _ = store.ConfirmHandover("H1", "Bob", "Lab B", now.Add(time.Hour))

// 查询：能看到来源样品、初始量和剩余量、当前持有人和地点、分装产生的子样、
// 按发生顺序排列的保管历史，以及待确认交接详情。
sample, _ = store.GetSample("S1")
handover, _ = store.GetHandover("H1")
```

## 规则要点

- 数量内部以千分之一毫升的整数存储，查询统一显示三位小数，计算不会因舍入多出或丢失样品。
- 剩余量为零的样品保留历史，但不能再分装或发起交接。
- 父样与子样此后的交接互不带动。
- 同一样品已有待确认交接时，不能再次发起交接或分装。
- 交接编号在本地数据中唯一：重复提交同一编号且内容完全相同的交出请求，返回原交接；
  沿用编号但改变样品、人员、地点或时间时拒绝。
- 已确认的交接重复提交相同接收信息仍返回原结果，不增加转手记录；接收信息不同则拒绝。
- 所有被拒绝的操作都不改变数量、当前位置、历史或待确认记录；并发操作同一样品
  不会导致超量分装或出现两条待确认交接。
- 不存在的样品或交接返回明确错误，不会顺带创建记录。
