<!-- source: RedHat-UX/red-hat-design-system@v5.0.0 CHANGELOG.md -->

## 5.0.0

### Major Changes

- abc1234: `<rh-card>`: removed the `legacy` attribute and `legacy` slot.

  Before:

  ```html
  <rh-card legacy="true"></rh-card>
  ```

  After:

  ```html
  <rh-card></rh-card>
  ```

- abc1234: `<rh-button>`: renamed the `color-palette` attribute.

  Before:

  ```html
  <rh-button color-palette="darker">Save</rh-button>
  ```

  After:

  ```html
  <rh-button variant="primary">Save</rh-button>
  ```
