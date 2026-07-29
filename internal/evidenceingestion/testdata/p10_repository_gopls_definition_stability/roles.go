package stability

import fmtalias "fmt"

const TopConstName = 1

var TopVarName = TopConstName

type EmbeddedDeclaration struct{}

type GenericContainer[TypeParameterName any] struct {
	EmbeddedDeclaration
	FieldName TypeParameterName
}

func GenericFunction[FunctionTypeParameter any](
	ParameterName FunctionTypeParameter,
) (ResultName FunctionTypeParameter) {
	return ParameterName
}

func (ReceiverName *GenericContainer[TypeParameterName]) MethodName(
	MethodParameter TypeParameterName,
) (MethodResult TypeParameterName) {
	const LocalConstName = TopConstName
	var LocalVarName TypeParameterName
	type LocalTypeName int

	ExistingMixedSlot := MethodParameter
	ExistingMixedSlot, NewMixedSlot := ExistingMixedSlot, MethodParameter
	for RangeKeyName, RangeValueName := range []TypeParameterName{NewMixedSlot} {
		LocalVarName = RangeValueName
		_ = RangeKeyName
	}

	_ = ExistingMixedSlot
LoopLabel:
	for {
		break LoopLabel
	}

	_ = LocalTypeName(LocalConstName)
	fmtalias.Println(ReceiverName.FieldName)
	return GenericFunction[TypeParameterName](LocalVarName)
}

func SameFileReference() int {
	value := GenericContainer[int]{FieldName: TopVarName}
	return value.MethodName(TopConstName)
}
