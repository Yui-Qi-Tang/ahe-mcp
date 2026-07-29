package stability

func CrossFileReference() int {
	value := GenericContainer[int]{FieldName: TopConstName}
	return value.MethodName(TopVarName)
}
