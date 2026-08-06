package upstream

// Provider identifiers select the upstream client family per 子源 (DESIGN §6)。
// 每条路由的上游是一个 Aggregator (aggregate.go)：单源直通、多源 (swfp) 并发聚合；
// 装配层 (cmd/relay) 按 kind 用 buildClient 构建每个子源 client。
const (
	ProviderGama        = "gama"
	ProviderIncome      = "income"
	ProviderRental      = "rental"
	ProviderBlacklist   = "blacklist"
	ProviderEntCredit   = "entcredit"   // swfp: 税务+发票四产品码聚合
	ProviderFaceCompare = "facecompare" // rlbd1: 人脸身份证比对一所 (数脉)
	ProviderIDVerify    = "idverify"    // sfzhy: 身份证三要素核验
	ProviderConsumeTxn  = "consumetxn"  // xfjy: 消费交易特征 (data-bean)
	ProviderComplaint   = "complaint"   // tsfx: 投诉分析识别名单 (kfongtech)
	ProviderSalesData   = "salesdata"   // swfp 第五子源: 销项数据 (凯盈云 crestv)
	ProviderLXScore     = "lxscore"     // lxf: 灵犀分 score_195_v1 (fullink)
)
