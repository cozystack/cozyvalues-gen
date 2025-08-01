## Case Description

Test case for complex object with `field`s declared inside them.

## Parameters
### Declaration through custom type

| Name  | Description           | Type     | Value |
| ----- | --------------------- | -------- | ----- |
| `foo` | Configuration for foo | `object` | `{}`  |


### Declaration with direct path

| Name            | Description                             | Type     | Value |
| --------------- | --------------------------------------- | -------- | ----- |
| `bar`           | Configuration for bar                   | `object` | `{}`  |
| `bar.db`        | Field with custom type declared locally | `object` | `{}`  |
| `bar.db.size`   | Sub-field declared with absolute path   | `string` | `""`  |
| `bar.db.volume` | Sub-field declared with absolute path   | `string` | `""`  |


