## Case Description

Tests for complex types, extending base ones.

- `quantity` — measure of CPU cores and memory.

## Parameters

### Quantity parameters

| Name                      | Description                                                     | Type       | Value    |
| ------------------------- | --------------------------------------------------------------- | ---------- | -------- |
| `quantityRequired`        | A required quantity value (CPU cores or RAM).                   | `quantity` | `null`   |
| `quantityRequiredEmpty`   | A required quantity value with empty string (CPU cores or RAM). | `quantity` | ``       |
| `quantityDefaultInt`      | A quantity default with a bare integer.                         | `quantity` | `2`      |
| `quantityDefaultStrInt`   | A quantity default with a quoted integer.                       | `quantity` | `2`      |
| `quantityDefaultCpuShare` | A quantity default with vCPU share.                             | `quantity` | `100m`   |
| `quantityDefaultRam`      | A quantity default with RAM size.                               | `quantity` | `500MiB` |

### Nullable quantity parameters

| Name                              | Description                                                     | Type        | Value    |
| --------------------------------- | --------------------------------------------------------------- | ----------- | -------- |
| `quantityNullable`                | A nullable quantity value.                                      | `*quantity` | `null`   |
| `quantityNullableRequiredEmpty`   | A nullable quantity value with empty string (CPU cores or RAM). | `*quantity` | ``       |
| `quantityNullableDefaultInt`      | A nullable quantity with a default bare integer.                | `*quantity` | `2`      |
| `quantityNullableDefaultStrInt`   | A nullable quantity with a default quoted integer.              | `*quantity` | `2`      |
| `quantityNullableDefaultCpuShare` | A nullable quantity with a default CPU share.                   | `*quantity` | `100m`   |
| `quantityNullableDefaultRam`      | A nullable quantity with a default RAM size.                    | `*quantity` | `500MiB` |
