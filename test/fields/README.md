## Case Description

Test case for complex object with `field`s declared inside them.

## Parameters

### Complex Object tests

| Name            | Description                                          | Type     | Value  |
| --------------- | ---------------------------------------------------- | -------- | ------ |
| `foo`           | Configuration for foo                                | `object` | `null` |
| `foo.db`        | Field with custom type declared locally              | `object` |        |
| `foo.db.size`   | Sub-field declared with path relative to custom type | `string` |        |
| `bar`           | Configuration for bar                                | `object` | `null` |
| `bar.db`        | Field with custom type declared locally              | `object` |        |
| `bar.db.size`   | Sub-field declared with path relative to custom type | `string` |        |
| `bar.db.volume` | Sub-field declared with absolute path                | `string` |        |
