package validation

import (
	"errors"

	"sigs.k8s.io/kind/pkg/commons"
)

var eksInstance *EKSValidator

type EKSValidator struct {
	descriptor commons.DescriptorFile
	secrets    commons.SecretsFile
}

func createEksInstance() *EKSValidator {
	// inicialización del singleton
	return &EKSValidator{}
}

func newEKSValidator() *EKSValidator {
	if eksInstance == nil {
		eksInstance = createEksInstance()
	}
	return eksInstance
}

func (v *EKSValidator) DescriptorFile(descriptorFile commons.DescriptorFile) {
	v.descriptor = descriptorFile
}

func (v *EKSValidator) SecretsFile(secrets commons.SecretsFile) {
	v.secrets = secrets
}

func (v *EKSValidator) Validate(fileType string) error {
	switch fileType {
	case "descriptor":
		descriptorEksValidations((*v).descriptor)
	case "secrets":
		secretsEksValidations((*v).secrets)
	default:
		return errors.New("Incorrect filetype validation")
	}
	return nil
}

func (v *EKSValidator) CommonsValidations() error {
	err := commonsValidations((*v).descriptor, (*v).secrets)
	if err != nil {
		return err
	}
	return nil
}

func descriptorEksValidations(descriptorFile commons.DescriptorFile) error {
	err := commonsDescriptorValidation(descriptorFile)
	if err != nil {
		return err
	}
	return nil
}

func secretsEksValidations(secretsFile commons.SecretsFile) error {

	return nil
}
