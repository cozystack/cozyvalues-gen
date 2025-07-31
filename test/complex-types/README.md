## Case Description

Tests for complex types, extending base ones.

- `quantity` — measure of CPU cores and memory.

## Parameters
### Quantity parameters

| Name                      | Description                                                     | Type     | Value    |
| ------------------------- | --------------------------------------------------------------- | -------- | -------- |
| `quantityRequired`        | A required quantity value (CPU cores or RAM).                   | `string` | `""`     |
| `quantityRequiredEmpty`   | A required quantity value with empty string (CPU cores or RAM). | `string` | `""`     |
| `quantityDefaultInt`      | A quantity default with a bare integer.                         | `string` | `2`      |
| `quantityDefaultStrInt`   | A quantity default with a quoted integer.                       | `string` | `2`      |
| `quantityDefaultCpuShare` | A quantity default with vCPU share.                             | `string` | `100m`   |
| `quantityDefaultRam`      | A quantity default with RAM size.                               | `string` | `500MiB` |


### Nullable quantity parameters

| Name                              | Description                                                     | Type      | Value    |
| --------------------------------- | --------------------------------------------------------------- | --------- | -------- |
| `quantityNullable`                | A nullable quantity value.                                      | `*string` | `null`   |
| `quantityNullableRequiredEmpty`   | A nullable quantity value with empty string (CPU cores or RAM). | `*string` | `""`     |
| `quantityNullableDefaultInt`      | A nullable quantity with a default bare integer.                | `*string` | `2`      |
| `quantityNullableDefaultStrInt`   | A nullable quantity with a default quoted integer.              | `*string` | `2`      |
| `quantityNullableDefaultCpuShare` | A nullable quantity with a default CPU share.                   | `*string` | `100m`   |
| `quantityNullableDefaultRam`      | A nullable quantity with a default RAM size.                    | `*string` | `500MiB` |


### Enumerated parameters

| Name                               | Description                          | Type     | Value   |
| ---------------------------------- | ------------------------------------ | -------- | ------- |
| `enumWithDefault`                  | Enum variable, defaults to "micro"   | `string` | `{}`    |
| `enumWithoutDefault`               | Enum variable with no default value. | `string` | `{}`    |
| `nested`                           | Element with nested enum fields      | `object` | `{}`    |
| `nested.enumWithCustomTypeDefault` | Enum variable, defaults to "micro"   | `string` | `micro` |


