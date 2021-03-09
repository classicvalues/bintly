package codegen

import (
	"fmt"
	"github.com/viant/toolbox"
	"io/ioutil"
	"strings"
)

type fieldGenerator func(session *session, field *toolbox.FieldInfo) (string, error)

type templateParameters struct {
	Method        string
	Field         string
	FieldType     string
	ReceiverAlias string
	TransientVar  string
	BaseType      string
	PointerNeeded bool
}

func Generate(options *Options) error {

	if err := options.Validate(); err != nil {
		return err
	}
	session := newSession(options)

	//
	session.addImport("github.com/viant/bintly")
	err := session.readPackageCode()
	if err != nil {
		return err
	}

	// then we generate code for the types given
	for _, rootType := range options.Types {
		if err := generateStructCoding(session, rootType); err != nil {
			return err
		}
	}
	dest := session.Dest
	err = ioutil.WriteFile(dest, []byte(strings.Join(session.structCodingCode, "")), 0644)
	session.structCodingCode = []string{}
	return nil

}

func generateStructCoding(session *session, typeName string) error {
	if ok := session.shallGenerateCode(typeName); !ok {
		return nil
	}
	enc, err := generateStructEncoding(session, typeName)
	if err != nil {
		return err
	}
	dec, err := generateStructDecoding(session, typeName)
	if err != nil {
		return err
	}

	receiver := strings.ToLower(typeName[0:1]) + " *" + typeName

	code, err := expandBlockTemplate(codingStructType, struct {
		Receiver      string
		EncodingCases string
		DecodingCases string
	}{receiver, enc, dec})
	if err != nil {
		return err
	}
	if !session.isBlockTemplateDone {
		session.isBlockTemplateDone = true
		code, err = expandBlockTemplate(fileCode, struct {
			Pkg     string
			Code    string
			Imports string
		}{session.pkg, code, session.getImports()})
	}

	session.structCodingCode = append(session.structCodingCode, code)
	//
	if session.Dest == "" {
		fmt.Print(session.structCodingCode)
		return nil
	}
	return err
}

func generateStructEncoding(sess *session, typeName string) (string, error) {
	return generateCoding(sess, typeName, false, func(sess *session, field *toolbox.FieldInfo) (string, error) {
		return "", fmt.Errorf("unsupported type: %s for field %v.%v", field.TypeName, typeName, field.Name)
	})
}

func generateStructDecoding(sess *session, typeName string) (string, error) {
	return generateCoding(sess, typeName, true, func(session *session, field *toolbox.FieldInfo) (string, error) {
		return "", fmt.Errorf("unsupported type: %s for field %v.%v", field.TypeName, typeName, field.Name)
	})

}

func generateCoding(sess *session, typeName string, isDecoder bool, fn fieldGenerator) (string, error) {

	baseTemplate := encodeBaseType
	derivedTemplate := encodeDerivedBaseType
	baseSliceTemplate := encodeBaseSliceType
	derivedSliceTemplate := encodeCustomSliceType
	structTemplate := encodeStructType
	customSliceTemplate := encodeSliceStructType
	if isDecoder {
		baseTemplate = decodeBaseType
		derivedTemplate = decodeDerivedBaseType
		baseSliceTemplate = decodeBaseSliceType
		derivedSliceTemplate = decodeCustomSliceType
		structTemplate = decodeStructType
		customSliceTemplate = decodeSliceStructType
	}
	typeInfo := sess.Type(typeName)
	if typeInfo == nil {
		return "", fmt.Errorf("failed to lookup '%s'", typeName)
	}
	var codings = make([]string, 0)
	fields := typeInfo.Fields()
	for _, field := range fields {
		receiverAlias := strings.ToLower(typeName[0:1])

		// base type
		if isBaseType(field.TypeName) {
			method := genCodingMethod(field.TypeName, field.IsPointer, field.IsSlice)
			code, err := expandFieldTemplate(baseTemplate, templateParameters{
				Method:        method,
				Field:         field.Name,
				ReceiverAlias: receiverAlias,
			})
			if err != nil {
				return "", err
			}
			codings = append(codings, code)
			continue
		}

		// derived type
		baseType, err := getBaseDerivedType(sess, field.TypeName)
		if baseType != "" {
			method := genCodingMethod(baseType, field.IsPointer, field.IsSlice)
			code, err := expandFieldTemplate(derivedTemplate, templateParameters{
				Method:        method,
				Field:         field.Name,
				FieldType:     field.TypeName,
				ReceiverAlias: receiverAlias,
				TransientVar:  toolbox.ToCaseFormat(field.Name, toolbox.CaseUpperCamel, toolbox.CaseLowerCamel),
				BaseType:      baseType,
			})
			if err != nil {
				return "", err
			}
			codings = append(codings, code)
			continue
		}

		// base slice type
		sliceType, err := getBaseSliceType(sess, field.TypeName)
		if sliceType != "" {
			method := genCodingMethod("[]"+sliceType, false, true)
			code, err := expandFieldTemplate(baseSliceTemplate, templateParameters{
				Method:        method,
				Field:         field.Name,
				FieldType:     field.TypeName,
				ReceiverAlias: receiverAlias,
				TransientVar:  toolbox.ToCaseFormat(field.Name, toolbox.CaseUpperCamel, toolbox.CaseLowerCamel),
				BaseType:      sliceType,
			})
			if err != nil {
				return "", err
			}
			codings = append(codings, code)
			continue
		}

		// derived slice type
		customSliceType, err := getDerivedSliceType(sess, field.TypeName)
		if customSliceType != "" {
			sess.addImport("unsafe")
			method := genCodingMethod("[]"+customSliceType, false, true)
			code, err := expandFieldTemplate(derivedSliceTemplate, templateParameters{
				Method:        method,
				Field:         field.Name,
				FieldType:     field.TypeName,
				ReceiverAlias: receiverAlias,
				TransientVar:  toolbox.ToCaseFormat(field.Name, toolbox.CaseUpperCamel, toolbox.CaseLowerCamel),
				BaseType:      customSliceType,
			})
			if err != nil {
				return "", err
			}
			codings = append(codings, code)
			continue
		}

		// struct type
		fieldType := sess.Type(getBaseFieldType(field.TypeName))
		if fieldType == nil {
			return "", fmt.Errorf("unsupported field type %v for field %v", fieldType, field)
		}
		if isStruct(fieldType) && !field.IsSlice && !field.IsPointerComponent {
			if err = generateStructCoding(sess, fieldType.Name); err != nil {
				return "", err
			}
			var isPointerStruct = true
			if isStruct(fieldType) {
				isPointerStruct = field.IsPointer
			}
			if field.IsSlice {
				isPointerStruct = field.IsPointerComponent
			}
			code, err := expandFieldTemplate(structTemplate, templateParameters{
				Method:        "Coder",
				Field:         field.Name,
				FieldType:     field.TypeName,
				ReceiverAlias: receiverAlias,
				PointerNeeded: isPointerStruct,
			})
			if err != nil {
				return "", err
			}
			codings = append(codings, code)
			continue

		}

		// struct slice
		if isStruct(fieldType) && field.IsSlice {
			if err = generateStructCoding(sess, fieldType.Name); err != nil {
				return "", err
			}
			fieldTypeName := field.TypeName[2:]
			if field.IsPointerComponent {
				fieldTypeName = field.TypeName[3:]
			}
			code, err := expandFieldTemplate(customSliceTemplate, templateParameters{
				Method:        "Coder",
				Field:         field.Name,
				FieldType:     fieldTypeName,
				ReceiverAlias: receiverAlias,
				TransientVar:  toolbox.ToCaseFormat(field.Name, toolbox.CaseUpperCamel, toolbox.CaseLowerCamel),
				PointerNeeded: !field.IsPointerComponent,
			})
			if err != nil {
				return "", err
			}
			codings = append(codings, code)
			continue

		}

		code, err := fn(sess, field)
		if err != nil {
			return "", err
		}
		codings = append(codings, code)
	}
	return strings.Join(codings, "\n"), nil
}

func getBaseDerivedType(s *session, typeName string) (string, error) {
	aType := s.Type(typeName)
	if aType == nil {
		return "", fmt.Errorf("alias type name %v is nil for type %v ", aType, typeName)
	}
	if aType.IsDerived {
		derived := aType.Derived
		if isBaseType(derived) {
			return derived, nil
		}
		derived, err := getBaseDerivedType(s, derived)
		if err != nil {
			return "", err
		}
		if isBaseType(derived) {
			return derived, nil
		}
	}
	return "", nil
}

func getBaseSliceType(s *session, typeName string) (string, error) {
	aType := s.Type(typeName)
	if aType == nil {
		return "", fmt.Errorf("alias type name %v is nil for type %v ", aType, typeName)
	}
	if aType.IsSlice && isBaseType(aType.ComponentType) {
		return aType.ComponentType, nil
	}
	return "", nil
}

func getDerivedSliceType(s *session, typeName string) (string, error) {
	aType := s.Type(typeName)
	if aType == nil {
		return "", fmt.Errorf("alias type name %v is nil for type %v ", aType, typeName)
	}

	if aType.IsSlice {
		cType, err := getBaseDerivedType(s, aType.ComponentType)
		if err != nil {
			return "", fmt.Errorf("can't find base type %v for componentType %v ", aType, aType.ComponentType)
		}
		return cType, nil
	}

	return "", nil
}

func genCodingMethod(baseType string, IsPointer bool, IsSlice bool) string {
	if strings.Contains(baseType, "time.Time") {
		baseType = strings.Replace(baseType, "time.", "", 1)
	}
	codingMethod := strings.Title(baseType)
	if IsPointer {
		codingMethod += "Ptr"
	}
	if IsSlice {
		codingMethod = codingMethod[2:]
		codingMethod += "s"
	}
	return codingMethod

}

func getBaseFieldType(fieldType string) string {
	if fieldType[0:3] == "[]*" {
		return fieldType[3:]
	}
	if fieldType[0:2] == "[]" {
		return fieldType[2:]
	}
	return fieldType
}
