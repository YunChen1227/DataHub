package upstream

// Provider constants name each upstream client family. 每条路由的上游是一个
// Aggregator (aggregate.go)：单源直通、多源并发聚合；对 orchestrator 完全透明。
const (
	ProviderGama        = "gama"        // x1: 伽马分层分
	ProviderIncome      = "income"      // v9/v8: 经济能力
	ProviderRental      = "rental"      // zlf: 租赁分V2-D / 守信
	ProviderBlacklist   = "blacklist"   // blk: 黑名单因子V35
	ProviderFaceCompare = "facecompare" // rlbd1/rlbd2: 人脸身份证比对一所
	ProviderIDVerify    = "idverify"    // sfzhy: 身份证三要素核验
	ProviderConsumeTxn  = "consumetxn"  // xfjy: 消费交易特征
	ProviderComplaint   = "complaint"   // tsfx: 投诉分析识别名单
	ProviderLXScore     = "lxscore"     // lxf: 灵犀分 score_195_v1
	ProviderIncomeAg    = "incomeag"    // grgjj: 收入A_g版 (主源)
	ProviderBgJJ        = "bgjj"        // grgjj: 备用公积金源 (jeoho，串行寻源的低优先级备源)
	ProviderBgPG        = "bgpg"        // grsb: 背景评估 BJPG-01
)
