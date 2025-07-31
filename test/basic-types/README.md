## Case Description

Test cases for basic types:

- `int`
- `bool`
- `string`

All types are checked in required and nullable variations.

## Parameters
### Integer parameters

| Name                     | Description                                   | Type   | Value  |
| ------------------------ | --------------------------------------------- | ------ | ------ |
| `testInt`                | Integer variable                              | `int`  | `0`    |
| `testIntDefault`         | Integer variable with default value           | `int`  | `10`   |
| `testIntNullable`        | Integer variable, nullable                    | `*int` | `null` |
| `testIntDefaultNullable` | Integer variable with default value, nullable | `*int` | `10`   |


### Boolean parameters

| Name                    | Description                                   | Type    | Value   |
| ----------------------- | --------------------------------------------- | ------- | ------- |
| `testBool`              | Boolean variable                              | `bool`  | `false` |
| `testBoolFalse`         | Boolean variable, defaults to false           | `bool`  | `false` |
| `testBoolTrue`          | Boolean variable, defaults to true            | `bool`  | `true`  |
| `testBoolNullable`      | Boolean variable, nullable                    | `*bool` | `null`  |
| `testBoolFalseNullable` | Boolean variable, defaults to false, nullable | `*bool` | `false` |
| `testBoolTrueNullable`  | Boolean variable, defaults to true, nullable  | `*bool` | `true`  |


### String parameters

| Name                        | Description                                  | Type      | Value          |
| --------------------------- | -------------------------------------------- | --------- | -------------- |
| `testString`                | String variable                              | `string`  | `""`           |
| `testStringEmpty`           | String variable, empty by default            | `string`  | `""`           |
| `testStringDefault`         | String variable with default value           | `string`  | `string value` |
| `testStringNullable`        | String variable, nullable                    | `*string` | `null`         |
| `testStringEmptyNullable`   | String variable, empty by default, nullable  | `*string` | `""`           |
| `testStringDefaultNullable` | String variable with default value, nullable | `*string` | `string value` |


