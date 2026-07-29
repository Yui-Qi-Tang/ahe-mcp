package detective

import (
	"reflect"
	"strings"
	"testing"
)

func TestPreparePlannerRecommendationConsumptionNormalizesAndHashes(t *testing.T) {
	input := PlannerRecommendationConsumptionInput{
		RequestID: " consume ",
		Recommendation: PlannerRecommendation{
			RunID:               " detective-run:" + strings.Repeat("a", 64) + " ",
			PlanningContextHash: " sha256:" + strings.Repeat("b", 64) + " ",
			Classification:      " query_source ",
			SourceBindingID:     " workspace-source:" + strings.Repeat("c", 64) + " ",
			CapabilityName:      " local-prd-text ",
			CapabilityVersion:   " v1 ",
			ReasonCode:          " initial_source ",
		},
	}
	first, err := preparePlannerRecommendationConsumption(input)
	if err != nil {
		t.Fatalf("preparePlannerRecommendationConsumption() error = %v", err)
	}
	second, err := preparePlannerRecommendationConsumption(input)
	if err != nil {
		t.Fatalf("second preparePlannerRecommendationConsumption() error = %v", err)
	}
	if !reflect.DeepEqual(first, second) || first.input.RequestID != "consume" || first.requestPayloadHash == "" {
		t.Fatalf("prepared requests = %+v / %+v", first, second)
	}

	changed := input
	changed.Recommendation.ReasonCode = OrchestrationReasonCoverageGap
	different, err := preparePlannerRecommendationConsumption(changed)
	if err != nil {
		t.Fatalf("changed preparePlannerRecommendationConsumption() error = %v", err)
	}
	if different.requestPayloadHash == first.requestPayloadHash {
		t.Fatal("changed recommendation produced the same payload hash")
	}
}

func TestPreparePlannerRecommendationConsumptionRejectsFailClosedInput(t *testing.T) {
	valid := PlannerRecommendationConsumptionInput{
		RequestID: "consume",
		Recommendation: PlannerRecommendation{
			RunID:               "detective-run:" + strings.Repeat("a", 64),
			PlanningContextHash: "sha256:" + strings.Repeat("b", 64),
			Classification:      PlannerClassificationQuerySource,
			SourceBindingID:     "workspace-source:" + strings.Repeat("c", 64),
			CapabilityName:      SourceCapabilityLocalPRDText,
			CapabilityVersion:   SourceCapabilityLocalPRDTextVersion,
			ReasonCode:          OrchestrationReasonInitialSource,
		},
	}
	tests := []struct {
		name   string
		mutate func(*PlannerRecommendationConsumptionInput)
		kind   ErrorKind
	}{
		{name: "missing request", mutate: func(input *PlannerRecommendationConsumptionInput) { input.RequestID = "" }, kind: ErrorInvalidInput},
		{name: "missing context", mutate: func(input *PlannerRecommendationConsumptionInput) { input.Recommendation.PlanningContextHash = "" }, kind: ErrorInvalidInput},
		{name: "abstain", mutate: func(input *PlannerRecommendationConsumptionInput) {
			input.Recommendation.Classification = PlannerClassificationAbstain
		}, kind: ErrorPlannerInsufficientCoverage},
		{name: "classification", mutate: func(input *PlannerRecommendationConsumptionInput) { input.Recommendation.Classification = "retry" }, kind: ErrorPlannerMalformedOutput},
		{name: "capability", mutate: func(input *PlannerRecommendationConsumptionInput) { input.Recommendation.CapabilityName = "jira" }, kind: ErrorUnsupportedCapability},
		{name: "reason", mutate: func(input *PlannerRecommendationConsumptionInput) {
			input.Recommendation.ReasonCode = OrchestrationReasonRefresh
		}, kind: ErrorPlannerUngroundedOutput},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := valid
			test.mutate(&input)
			_, err := preparePlannerRecommendationConsumption(input)
			assertDetectiveKind(t, err, test.kind)
		})
	}
}

func TestConsumePlannerRecommendationRequiresPool(t *testing.T) {
	_, err := ConsumePlannerRecommendation(t.Context(), nil, PlannerRecommendationConsumptionInput{})
	assertDetectiveKind(t, err, ErrorInvalidInput)
}
