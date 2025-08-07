## Case Description

Cases for lists of `int`, `string`, and `object`.

## Parameters
### Integer lists

| Name                      | Description                                       | Type     | Value        |
| ------------------------- | ------------------------------------------------- | -------- | ------------ |
| `intList`                 | A required list of integers, empty.               | `[]int`  | `[]`         |
| `intListSingle`           | A list of integers with one value.                | `[]int`  | `[80]`       |
| `intListMultiple`         | A list of integers with multiple values.          | `[]int`  | `[80, 8080]` |
| `intListNullable`         | A nullable list of integers, empty.               | `[]*int` | `[]`         |
| `intListNullableSingle`   | A nullable list of integers with one value.       | `[]*int` | `[80]`       |
| `intListNullableMultiple` | A nullable list of integers with multiple values. | `[]*int` | `[80, 8080]` |


### String lists

| Name                          | Description                                      | Type        | Value            |
| ----------------------------- | ------------------------------------------------ | ----------- | ---------------- |
| `stringList`                  | A required list of strings, empty.               | `[]string`  | `[]`             |
| `stringListSingle`            | A required list of strings with one value.       | `[]string`  | `[user1]`        |
| `stringListMultiple`          | A required list of strings with multiple values. | `[]string`  | `[user1, user2]` |
| `stringListNullable`          | A nullable list of strings, empty.               | `*[]string` | `null`           |
| `stringListNullableSingle`    | A nullable list of strings with one value.       | `*[]string` | `[user1]`        |
| `stringListNullableMultiple`  | A nullable list of strings with multiple values. | `*[]string` | `[user1, user2]` |
| `stringListNullable2`         | A nullable list of strings, empty.               | `[]*string` | `[]`             |
| `stringListNullableSingle2`   | A nullable list of strings with one value.       | `[]*string` | `[user1]`        |
| `stringListNullableMultiple2` | A nullable list of strings with multiple values. | `[]*string` | `[user1, user2]` |


### Object lists

| Name                     | Description                                  | Type             | Value                                              |
| ------------------------ | -------------------------------------------- | ---------------- | -------------------------------------------------- |
| `objectList`             | List of nested objects                       | `[]nestedObject` | `[]`                                               |
| `objectList[0].name`     | String field                                 | `string`         | `example`                                          |
| `objectList[0].foo`      | Object field with custom declared type       | `object`         | `{}`                                               |
| `objectList[0].foo.fizz` | Nested int field                             | `int`            | `10`                                               |
| `objectList[0].foo.buzz` | Nested quantity field, nullable              | `*string`        | `1GiB`                                             |
| `objectList[0].bar`      | Another object field of custom declared type | `object`         | `{}`                                               |
| `objectList[0].bar.fizz` | Nested int field                             | `int`            | `20`                                               |
| `objectList[0].bar.buzz` | Nested quantity field, nullable              | `*string`        | `2GiB`                                             |
| `objectList[1].name`     | String field                                 | `string`         | `example 2 - not expected to appear in the README` |
| `objectList[1].foo`      | Object field with custom declared type       | `object`         | `{}`                                               |
| `objectList[1].foo.fizz` | Nested int field                             | `int`            | `10`                                               |
| `objectList[1].foo.buzz` | Nested quantity field, nullable              | `*string`        | `1GiB`                                             |
| `objectList[1].bar`      | Another object field of custom declared type | `object`         | `{}`                                               |
| `objectList[1].bar.fizz` | Nested int field                             | `int`            | `20`                                               |
| `objectList[1].bar.buzz` | Nested quantity field, nullable              | `*string`        | `2GiB`                                             |


