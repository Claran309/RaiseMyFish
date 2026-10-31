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
	ID                         string             `json:"id"`
	RawSHA256                  string             `json:"raw_sha256"`
	DeliveredSHA256            string             `json:"delivered_sha256"`
	RawFailures                []string           `json:"raw_failures"`
	DeliveredFailures          []string           `json:"delivered_failures"`
	RawStyleObservations       []StyleObservation `json:"raw_style_observations,omitempty"`
	DeliveredStyleObservations []StyleObservation `json:"delivered_style_observations,omitempty"`
	RawPass                    bool               `json:"raw_pass"`
	DeliveredPass              bool               `json:"delivered_pass"`
}

// RescoreReport 明确这是旧输出的数学重评分，不是新模型验证。
type RescoreReport struct {
	SourceKind             string              `json:"source_kind"`
	SourceOriginalKind     string              `json:"source_original_kind"`
	SourceRunID            string              `json:"source_run_id"`
	SourceReportSHA256     string              `json:"source_report_sha256"`
	SourceRulesSHA256      string              `json:"source_rules_sha256"`
	SourceCasesSHA256      string              `json:"source_cases_sha256,omitempty"`
	RevisedCasesSHA256     string              `json:"revised_cases_sha256,omitempty"`
	ExpectationChanges     []ExpectationChange `json:"expectation_changes,omitempty"`
	SourceProviderModel    string              `json:"source_provider_model"`
	RulesVersion           string              `json:"rules_version"`
	RulesRevision          string              `json:"rules_revision,omitempty"`
	PassRateBasis          string              `json:"pass_rate_basis,omitempty"`
	ManualReviewRequired   bool                `json:"manual_review_required,omitempty"`
	ManualReviewStatus     string              `json:"manual_review_status,omitempty"`
	RawStyleMetrics        []StyleMetric       `json:"raw_style_metrics,omitempty"`
	DeliveredStyleMetrics  []StyleMetric       `json:"delivered_style_metrics,omitempty"`
	RawSafetySummary       *SafetySummary      `json:"raw_safety_summary,omitempty"`
	DeliveredSafetySummary *SafetySummary      `json:"delivered_safety_summary,omitempty"`
	RulesSHA256            string              `json:"rules_sha256"`
	ActualModelCalls       int                 `json:"actual_model_calls"`
	NewModelValidation     bool                `json:"new_model_validation"`
	Total                  int                 `json:"total"`
	RawPassed              int                 `json:"raw_passed"`
	DeliveredPassed        int                 `json:"delivered_passed"`
	RawPassRate            float64             `json:"raw_pass_rate"`
	DeliveredPassRate      float64             `json:"delivered_pass_rate"`
	Results                []RescoreResult     `json:"results"`
}

// ExpectationChange 明确标注只改测量目标，没有改变原始模型输入或输出。
type ExpectationChange struct {
	ID     string   `json:"id"`
	Before []string `json:"before"`
	After  []string `json:"after"`
}

// RescoreExisting 先校验来源/原文/用例/规则hash，只向独立新目录写入，不接收Generator。
func RescoreExisting(sourceDir, outputDir string, scorer *Scorer) (RescoreReport, error) {
	return rescore(sourceDir, outputDir, scorer, "", "")
}

// RescoreWithExpectations 只接受hash锁定且仅expect不同的同一组输入，绝不调用模型。
func RescoreWithExpectations(sourceDir, outputDir string, scorer *Scorer, revisedPath, revisedSHA256 string) (RescoreReport, error) {
	if revisedPath == "" || revisedSHA256 == "" {
		return RescoreReport{}, errors.New("修订expect必须指定文件及SHA256")
	}
	return rescore(sourceDir, outputDir, scorer, revisedPath, revisedSHA256)
}

func rescore(sourceDir, outputDir string, scorer *Scorer, revisedPath, revisedSHA256 string) (result RescoreReport, returnErr error) {
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
	var revisedData []byte
	var changes []ExpectationChange
	effectiveByID := make(map[string]Case, len(cases))
	for _, c := range cases {
		effectiveByID[c.ID] = c
	}
	if revisedPath != "" {
		revisedData, err = os.ReadFile(revisedPath)
		if err != nil {
			return result, fmt.Errorf("读取修订用例：%w", err)
		}
		if Digest(revisedData) != revisedSHA256 {
			return result, errors.New("修订用例SHA256不符")
		}
		revised, err := parseCases(revisedData)
		if err != nil {
			return result, err
		}
		if err := scorer.ValidateCases(revised); err != nil {
			return result, err
		}
		if len(revised) != len(cases) {
			return result, errors.New("修订用例数量不得改变")
		}
		for i, c := range revised {
			before, after := cases[i], c
			expect := after.Expect
			after.Expect = before.Expect
			if !reflect.DeepEqual(before, after) {
				return result, errors.New("仅允许修订expect，不得改ID/顺序/输入/场景/kind/forbid")
			}
			if !reflect.DeepEqual(before.Expect, expect) {
				changes = append(changes, ExpectationChange{ID: c.ID, Before: before.Expect, After: expect})
			}
			effectiveByID[c.ID] = c
		}
	}
	byID := make(map[string]Case, len(cases))
	for _, c := range cases {
		byID[c.ID] = c
	}
	result = RescoreReport{SourceKind: "rescore_existing_model_outputs", SourceRunID: original.RunID, SourceReportSHA256: Digest(data), SourceRulesSHA256: original.RulesSHA256, SourceProviderModel: original.ProviderModel, RulesVersion: scorer.Version(), RulesSHA256: Digest(scorer.Snapshot()), Total: original.Total}
	result.SourceOriginalKind = original.SourceKind
	result.RulesRevision = scorer.book.Revision
	if scorer.book.ScoringMode == "safety-only" {
		result.PassRateBasis = "automated_safety"
		result.ManualReviewRequired = true
		result.ManualReviewStatus = "pending"
	}
	result.SourceCasesSHA256 = caseHash
	if revisedData != nil {
		result.RevisedCasesSHA256 = Digest(revisedData)
		result.ExpectationChanges = changes
	}
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
		if scorer.book.ScoringMode == "safety-only" && (row.Error != "" || strings.TrimSpace(row.RawOutput) == "" || strings.TrimSpace(row.DeliveredOutput) == "") {
			return RescoreReport{}, errors.New("来源含生成失败或空输出，无法完成安全评估，不记作风格或安全违规")
		}
		if Digest([]byte(row.RawOutput)) != row.RawSHA256 || Digest([]byte(row.DeliveredOutput)) != row.DeliveredSHA256 {
			return RescoreReport{}, errors.New("来源输出hash不匹配")
		}
		effective := effectiveByID[c.ID]
		r := RescoreResult{ID: c.ID, RawSHA256: row.RawSHA256, DeliveredSHA256: row.DeliveredSHA256, RawFailures: scorer.Evaluate(effective, row.RawOutput), DeliveredFailures: scorer.Evaluate(effective, row.DeliveredOutput)}
		if scorer.book.ScoringMode == "safety-only" {
			r.RawStyleObservations = scorer.Assess(effective, row.RawOutput).StyleObservations
			r.DeliveredStyleObservations = scorer.Assess(effective, row.DeliveredOutput).StyleObservations
		}
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
	if scorer.book.ScoringMode == "safety-only" {
		rawGroups, deliveredGroups := make([][]StyleObservation, 0, len(result.Results)), make([][]StyleObservation, 0, len(result.Results))
		for _, row := range result.Results {
			rawGroups = append(rawGroups, row.RawStyleObservations)
			deliveredGroups = append(deliveredGroups, row.DeliveredStyleObservations)
		}
		result.RawStyleMetrics = scorer.styleMetrics(rawGroups)
		result.DeliveredStyleMetrics = scorer.styleMetrics(deliveredGroups)
		var rawFailures, deliveredFailures, emptyErrors [][]string
		for _, row := range result.Results {
			rawFailures = append(rawFailures, row.RawFailures)
			deliveredFailures = append(deliveredFailures, row.DeliveredFailures)
			emptyErrors = append(emptyErrors, nil)
		}
		result.RawSafetySummary = safetySummary(result.Total, rawFailures, emptyErrors)
		result.DeliveredSafetySummary = safetySummary(result.Total, deliveredFailures, emptyErrors)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return RescoreReport{}, err
	}
	if err := os.Mkdir(target, 0700); err != nil {
		return RescoreReport{}, fmt.Errorf("重评目录必须新建：%w", err)
	}
	// 产物名固定且限于新建目录，避免修订文件内容污染输出路径。
	outputRoot, err := os.OpenRoot(target)
	if err != nil {
		return RescoreReport{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, outputRoot.Close()) }()
	if err := outputRoot.WriteFile("rules.json", scorer.Snapshot(), 0600); err != nil {
		return RescoreReport{}, err
	}
	if revisedData != nil {
		if err := outputRoot.WriteFile("cases-revision.jsonl", revisedData, 0600); err != nil {
			return RescoreReport{}, err
		}
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return RescoreReport{}, err
	}
	if err := outputRoot.WriteFile("report.json", append(encoded, '\n'), 0600); err != nil {
		return RescoreReport{}, err
	}
	text := fmt.Sprintf("# %s\n\nsource_kind=%s；source_original_kind=%s；source_run=%s；模型调用0；不是新模型验证。\n\n规则%s，原始%d/%d=%.1f%%；投递%d/%d=%.1f%%。\n", title, result.SourceKind, result.SourceOriginalKind, result.SourceRunID, result.RulesVersion, result.RawPassed, result.Total, result.RawPassRate*100, result.DeliveredPassed, result.Total, result.DeliveredPassRate*100)
	if revisedData != nil {
		text += fmt.Sprintf("\n本目录使用修订expect（%d条变更），只重测旧输出；source_cases_sha256=%s；revised_cases_sha256=%s。\n", len(changes), caseHash, result.RevisedCasesSHA256)
	}
	if result.PassRateBasis == "automated_safety" {
		text += "\n通过率仅为自动安全层；人工硬核查待完成，不是最终验收结论。\n"
		text += styleMarkdown(result.RawStyleMetrics)
	}
	if err := outputRoot.WriteFile("report.md", []byte(text), 0600); err != nil {
		return RescoreReport{}, err
	}
	return result, nil
}
