package sourcepilot

// GuidedReference links a whole paragraph to recorded claim checks and body
// observations. Empty lists describe selection only, never evidential relevance.
type GuidedReference struct {
	Segment      Segment `json:"segment"`
	Claims       []int   `json:"claims"`
	Observations []int   `json:"observations"`
}

// GuidedReferences projects reference usage in source order without a model or
// text reduction. Callers reading stored JSON must validate the report first.
func GuidedReferences(report GuidedReport) []GuidedReference {
	rows := make([]GuidedReference, len(report.Projection.Segments))
	for i, segment := range report.Projection.Segments {
		row := GuidedReference{Segment: segment, Claims: []int{}, Observations: []int{}}
		for _, assessment := range report.Assessments {
			for _, number := range assessment.Segments {
				if number == segment.Number {
					row.Claims = append(row.Claims, assessment.Claim)
					break
				}
			}
		}
		for index, observation := range report.Observations {
			for _, number := range observation.Segments {
				if number == segment.Number {
					row.Observations = append(row.Observations, index+1)
					break
				}
			}
		}
		rows[i] = row
	}
	return rows
}
