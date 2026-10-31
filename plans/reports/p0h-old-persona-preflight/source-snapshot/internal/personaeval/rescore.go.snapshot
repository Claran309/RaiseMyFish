package personaeval

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

// RescoreResult 只引用原文hash，不复制或改写旧输出。
type RescoreResult struct {
	ID                string   `json:"id"`
	RawSHA256         string   `json:"raw_sha256"`
	DeliveredSHA256   string   `json:"delivered_sha256"`
	RawFailures       []string `json:"raw_failures"`
	DeliveredFailures []string `json:"delivered_failures"`
	RawPass           bool     `json:"raw_pass"`
	DeliveredPass     bool     `json:"delivered_pass"`
}

// RescoreReport 明确这是旧输出的数学重评分，不是新模型验证。
type RescoreReport struct {
	SourceKind          string          `json:"source_kind"`
	SourceOriginalKind  string          `json:"source_original_kind"`
	SourceRunID         string          `json:"source_run_id"`
	SourceReportSHA256  string          `json:"source_report_sha256"`
	SourceRulesSHA256   string          `json:"source_rules_sha256"`
	SourceProviderModel string          `json:"source_provider_model"`
	RulesVersion        string          `json:"rules_version"`
	RulesSHA256         string          `json:"rules_sha256"`
	ActualModelCalls    int             `json:"actual_model_calls"`
	NewModelValidation  bool            `json:"new_model_validation"`
	Total               int             `json:"total"`
	RawPassed           int             `json:"raw_passed"`
	DeliveredPassed     int             `json:"delivered_passed"`
	RawPassRate         float64         `json:"raw_pass_rate"`
	DeliveredPassRate   float64         `json:"delivered_pass_rate"`
	Results             []RescoreResult `json:"results"`
}

// RescoreExisting 先校验来源/原文/用例/规则hash，只向独立新目录写入，不接收Generator。
func RescoreExisting(sourceDir, outputDir string, scorer *Scorer) (RescoreReport, error) {
	var result RescoreReport
	if scorer == nil {
		return result, errors.New("评分器不能为空")
	}
	source, err := filepath.Abs(sourceDir)
	if err != nil {
		return result, err
	}
	target, err := filepath.Abs(outputDir)
	if err != nil {
		return result, err
	}
	rel, err := filepath.Rel(source, target)
	if err != nil {
		return result, err
	}
	if rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return result, errors.New("离线重评必须使用旧run之外的独立目录")
	}
	data, err := os.ReadFile(filepath.Join(source, "report.json"))
	if err != nil {
		return result, err
	}
	var original Report
	if err := json.Unmarshal(data, &original); err != nil {
		return result, err
	}
	if !original.Complete || original.Fatal || original.Interrupted || original.Total < 1 || original.Completed != original.Total || len(original.Results) != original.Total {
		return result, errors.New("来源运行不完整，不能当完整样本重评")
	}
	if original.SourceKind != "model_provider" && original.SourceKind != "test" {
		return result, errors.New("来源不是原始模型输出报告")
	}
	oldRules, err := os.ReadFile(filepath.Join(source, "rules.json"))
	if err != nil {
		return result, err
	}
	if Digest(oldRules) != original.RulesSHA256 {
		return result, errors.New("来源评分规则hash不匹配")
	}
	caseData, err := os.ReadFile(filepath.Join(source, "cases.jsonl"))
	if err != nil {
		return result, err
	}
	caseHash := Digest(caseData)
	found := false
	for _, file := range original.Files {
		if file.Name == "cases.jsonl" && file.SHA256 == caseHash && file.Bytes == len(caseData) {
			found = true
		}
	}
	if !found {
		return result, errors.New("来源用例快照hash不匹配")
	}
	cases, err := parseCases(caseData)
	if err != nil {
		return result, err
	}
	if err := scorer.ValidateCases(cases); err != nil {
		return result, err
	}
	if len(cases) != original.Total {
		return result, errors.New("来源用例和输出数量不符")
	}
	byID := make(map[string]Case, len(cases))
	for _, c := range cases {
		byID[c.ID] = c
	}
	result = RescoreReport{SourceKind: "rescore_existing_model_outputs", SourceRunID: original.RunID, SourceReportSHA256: Digest(data), SourceRulesSHA256: original.RulesSHA256, SourceProviderModel: original.ProviderModel, RulesVersion: scorer.Version(), RulesSHA256: Digest(scorer.Snapshot()), Total: original.Total}
	result.SourceOriginalKind = original.SourceKind
	title := "旧真实输出离线重评分"
	if original.SourceKind == "test" {
		result.SourceKind = "rescore_test_outputs"
		title = "测试输出离线重评分"
	}
	seen := make(map[string]bool, len(cases))
	for _, row := range original.Results {
		c, ok := byID[row.Case.ID]
		if !ok || seen[c.ID] {
			return RescoreReport{}, errors.New("来源输出ID缺失或重复")
		}
		seen[c.ID] = true
		if !reflect.DeepEqual(c, row.Case) {
			return RescoreReport{}, errors.New("来源用例内容与字节快照不符")
		}
		if row.Redacted {
			return RescoreReport{}, errors.New("脱敏原文不可自动恢复重评，需人工保留失败")
		}
		if Digest([]byte(row.RawOutput)) != row.RawSHA256 || Digest([]byte(row.DeliveredOutput)) != row.DeliveredSHA256 {
			return RescoreReport{}, errors.New("来源输出hash不匹配")
		}
		r := RescoreResult{ID: c.ID, RawSHA256: row.RawSHA256, DeliveredSHA256: row.DeliveredSHA256, RawFailures: scorer.Evaluate(c, row.RawOutput), DeliveredFailures: scorer.Evaluate(c, row.DeliveredOutput)}
		if row.Error != "" {
			r.RawFailures = append(r.RawFailures, "generation_error")
			r.DeliveredFailures = append(r.DeliveredFailures, "generation_error")
		}
		r.RawPass = len(r.RawFailures) == 0
		r.DeliveredPass = len(r.DeliveredFailures) == 0
		if r.RawPass {
			result.RawPassed++
		}
		if r.DeliveredPass {
			result.DeliveredPassed++
		}
		result.Results = append(result.Results, r)
	}
	result.RawPassRate = float64(result.RawPassed) / float64(result.Total)
	result.DeliveredPassRate = float64(result.DeliveredPassed) / float64(result.Total)
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return RescoreReport{}, err
	}
	if err := os.Mkdir(target, 0700); err != nil {
		return RescoreReport{}, fmt.Errorf("重评目录必须新建：%w", err)
	}
	if err := os.WriteFile(filepath.Join(target, "rules.json"), scorer.Snapshot(), 0600); err != nil {
		return RescoreReport{}, err
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return RescoreReport{}, err
	}
	if err := os.WriteFile(filepath.Join(target, "report.json"), append(encoded, '\n'), 0600); err != nil {
		return RescoreReport{}, err
	}
	text := fmt.Sprintf("# %s\n\nsource_kind=%s；source_original_kind=%s；source_run=%s；模型调用0；不是新模型验证。\n\n规则%s，原始%d/%d=%.1f%%；投递%d/%d=%.1f%%。\n", title, result.SourceKind, result.SourceOriginalKind, result.SourceRunID, result.RulesVersion, result.RawPassed, result.Total, result.RawPassRate*100, result.DeliveredPassed, result.Total, result.DeliveredPassRate*100)
	if err := os.WriteFile(filepath.Join(target, "report.md"), []byte(text), 0600); err != nil {
		return RescoreReport{}, err
	}
	return result, nil
}
