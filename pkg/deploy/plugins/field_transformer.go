package plugins

import (
	"fmt"
	"strings"

	"sigs.k8s.io/kustomize/api/resmap"
	"sigs.k8s.io/kustomize/api/resource"
)

// FieldMapping defines a single field mapping
type FieldMapping struct {
	// SourceValue is the value to copy to the target field
	SourceValue interface{} `json:"sourceValue"`
	// TargetField is the dot-notation path to the field in the target object
	TargetField string `json:"targetField"`
	// TargetKind is the kind of resource to apply the transformation to
	TargetKind string `json:"targetKind"`
	// CreateIfNotExists will create the target field if it doesn't exist
	CreateIfNotExists bool `json:"createIfNotExists,omitempty"`
}

// FieldTransformerConfig holds configuration for the field transformer
type FieldTransformerConfig struct {
	// Mappings is a list of field mappings to apply
	Mappings []FieldMapping `json:"mappings"`
}

// CreateFieldTransformer creates a transformer plugin that copies values between fields
func CreateFieldTransformer(config FieldTransformerConfig) resmap.TransformerPlugin {
	return &fieldTransformer{
		config: config,
	}
}

type fieldTransformer struct {
	config FieldTransformerConfig
}

// isEmpty checks if a value is nil or empty
func isEmpty(v interface{}) bool {
	if v == nil {
		return true
	}

	switch val := v.(type) {
	case string:
		return val == ""
	case map[string]interface{}:
		return len(val) == 0
	case []interface{}:
		return len(val) == 0
	}

	return false
}

func (t *fieldTransformer) Transform(m resmap.ResMap) error {
	// Process each mapping
	for _, mapping := range t.config.Mappings {
		// Skip if source value is empty
		if isEmpty(mapping.SourceValue) {
			continue
		}

		// Apply the transformation to matching resources
		for _, res := range m.Resources() {
			if res.GetKind() != mapping.TargetKind {
				continue
			}

			// Set the target field
			if err := t.setTargetField(res, mapping.SourceValue, mapping); err != nil {
				return fmt.Errorf("failed to set target field for mapping %s: %w", mapping.TargetField, err)
			}
		}
	}

	return nil
}

func (t *fieldTransformer) Config(h *resmap.PluginHelpers, _ []byte) error {
	return nil
}

func (t *fieldTransformer) setTargetField(res *resource.Resource, value interface{}, mapping FieldMapping) error {
	// Split the target field path
	fields := strings.Split(mapping.TargetField, ".")

	// Navigate to the target field
	current, err := res.Map()
	if err != nil {
		return fmt.Errorf("failed to get resource map: %w", err)
	}

	for _, field := range fields[:len(fields)-1] {
		next, ok := current[field]
		if !ok {
			if !mapping.CreateIfNotExists {
				return fmt.Errorf("field %s not found", field)
			}
			next = make(map[string]interface{})
			current[field] = next
		}

		nextMap, ok := next.(map[string]interface{})
		if !ok {
			return fmt.Errorf("field %s is not a map", field)
		}

		current = nextMap
	}

	// Set the value
	current[fields[len(fields)-1]] = value
	return nil
}
