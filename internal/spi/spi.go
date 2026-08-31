// Package spi 提供 SPI 编号与 contract（项目合同号）之间的推导逻辑。
//
// SPI 格式约定：PM-<contract>-<序号>，例如：
//
//	PM-ACME2026001-01  ->  contract = ACME2026001
package spi

import (
	"fmt"
	"regexp"
	"strings"
)

// SPI 固定前缀。
const Prefix = "PM-"

// spiRe 严格匹配 PM-<字母数字>-<数字>。
var spiRe = regexp.MustCompile(`^PM-([A-Za-z0-9]+)-([0-9]+)$`)

// Normalize 去除首尾空白。
func Normalize(s string) string {
	return strings.TrimSpace(s)
}

// DeriveContract 从 SPI 推导 contract：PM-ACME2026001-01 -> ACME2026001。
func DeriveContract(spiValue string) (string, error) {
	spiValue = Normalize(spiValue)
	m := spiRe.FindStringSubmatch(spiValue)
	if m == nil {
		return "", fmt.Errorf("无法从 spi %q 推导 contract（应为 PM-<contract>-<序号>，例如 PM-ACME2026001-01）", spiValue)
	}
	return m[1], nil
}

// ResolveContract 返回显式 contract；为空时从 spi 推导。
func ResolveContract(contract, spiValue string) (string, error) {
	contract = strings.TrimSpace(contract)
	if contract != "" {
		return contract, nil
	}
	return DeriveContract(spiValue)
}

// ValidateSPI 校验 SPI 格式是否合法。
func ValidateSPI(spiValue string) error {
	spiValue = Normalize(spiValue)
	if spiValue == "" {
		return fmt.Errorf("spi 不能为空")
	}
	if !spiRe.MatchString(spiValue) {
		return fmt.Errorf("spi 格式非法 %q（应为 PM-<contract>-<序号>，例如 PM-ACME2026001-01）", spiValue)
	}
	return nil
}

// ContractFromSPI 兼容旧名（等价于 DeriveContract）。
func ContractFromSPI(s string) (string, error) {
	return DeriveContract(s)
}

// TargetKey 计算目标 TOS key 前缀（不含尾部处理，调用方负责加 '/'）。
func TargetKey(contract, spiID string) string {
	return contract + "/" + spiID
}
