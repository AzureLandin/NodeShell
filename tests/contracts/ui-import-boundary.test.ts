import { readdirSync, readFileSync } from 'node:fs'
import { basename, dirname, relative, resolve, sep } from 'node:path'
import { fileURLToPath } from 'node:url'
import ts from 'typescript'
import { describe, expect, it } from 'vitest'

const rendererRoot = fileURLToPath(new URL('../../src/renderer/src/', import.meta.url))

function sourceFiles(directory = rendererRoot, includeUi = false): string[] {
  return readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
    const path = resolve(directory, entry.name)
    if (entry.isDirectory()) {
      if (!includeUi && relative(rendererRoot, path).split(sep)[0] === 'ui') return []
      return sourceFiles(path, includeUi)
    }
    return /\.tsx?$/.test(entry.name) ? [path] : []
  })
}

function restrictedImports(
  filePath: string,
  isRestricted: (moduleName: string) => boolean
): string[] {
  const kind = filePath.endsWith('.tsx') ? ts.ScriptKind.TSX : ts.ScriptKind.TS
  const source = ts.createSourceFile(
    basename(filePath),
    readFileSync(filePath, 'utf8'),
    ts.ScriptTarget.Latest,
    true,
    kind
  )
  const imports: string[] = []
  const visit = (node: ts.Node): void => {
    if (
      (ts.isImportDeclaration(node) || ts.isExportDeclaration(node)) &&
      node.moduleSpecifier &&
      ts.isStringLiteral(node.moduleSpecifier)
    ) {
      const moduleName = node.moduleSpecifier.text
      if (isRestricted(moduleName)) {
        imports.push(moduleName)
      }
    }
    if (
      ts.isCallExpression(node) &&
      node.expression.kind === ts.SyntaxKind.ImportKeyword &&
      node.arguments.length === 1 &&
      ts.isStringLiteral(node.arguments[0])
    ) {
      const moduleName = node.arguments[0].text
      if (isRestricted(moduleName)) {
        imports.push(moduleName)
      }
    }
    ts.forEachChild(node, visit)
  }
  visit(source)
  return imports
}

describe('Base UI import boundary', () => {
  it('keeps direct Base UI imports inside ui/', () => {
    const violations = sourceFiles().flatMap((filePath) =>
      restrictedImports(
        filePath,
        (moduleName) => moduleName === '@base-ui/react' || moduleName.startsWith('@base-ui/react/')
      ).map((moduleName) => `${relative(rendererRoot, filePath)}: ${moduleName}`)
    )

    expect(violations).toEqual([])
  })

  it('keeps notification manager and primitive imports behind the application notification entry', () => {
    const violations = sourceFiles().flatMap((filePath) =>
      restrictedImports(filePath, (moduleName) =>
        /(?:^|\/)ui\/(?:toast|use-toast-manager|notification-context)(?:\.[jt]sx?)?$/.test(
          moduleName
        )
      ).map((moduleName) => `${relative(rendererRoot, filePath)}: ${moduleName}`)
    )

    expect(violations).toEqual([])
  })

  it('uses public feature entries across features and keeps feature code independent of app composition', () => {
    const violations = sourceFiles(rendererRoot, true).flatMap((filePath) => {
      const owner = relative(rendererRoot, filePath).split(sep)
      return restrictedImports(filePath, (moduleName) => {
        if (!moduleName.startsWith('.')) return false
        const target = relative(rendererRoot, resolve(dirname(filePath), moduleName)).split(sep)
        if (
          owner[0] === 'features' &&
          (target[0] === 'app' || /^App(?:\.[jt]sx?)?$/.test(target[0]))
        )
          return true
        if (owner[0] === 'ui' && ['features', 'app'].includes(target[0])) return true
        if (target[0] !== 'features') return false
        if (owner[0] === 'features' && owner[1] === target[1]) return false
        return target.length > 2 && !['index', 'index.ts'].includes(target[2])
      }).map((moduleName) => `${relative(rendererRoot, filePath)}: ${moduleName}`)
    })
    expect(violations).toEqual([])
  })
})
