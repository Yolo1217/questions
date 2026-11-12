package dto

// RegisterCommand 是注册用户用例的入参。
// handler 层负责把 gRPC/HTTP 请求转换成该结构，service 层只读取整理后的业务字段。
type RegisterCommand struct {
	Account     string
	DisplayName string
	Password    string
	Role        string
}

// SeedUserCommand 是启动时预置账号的入参（账号已存在则跳过，保证幂等）。
type SeedUserCommand struct {
	Account     string
	DisplayName string
	Password    string
	Role        string
}

// LoginCommand 是用户登录用例的入参。
// 该结构不包含任何协议对象，方便 service 层做独立单元测试。
type LoginCommand struct {
	Account  string
	Password string
}
