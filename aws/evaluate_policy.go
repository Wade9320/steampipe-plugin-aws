package aws

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
)

type EvaluatedAction struct {
	prefix     string
	priviledge string
	matcher    string
}

type EvaluatedPrincipal struct {
	allowedPrincipalFederatedIdentitiesSet map[string]bool
	allowedPrincipalServicesSet            map[string]bool
	allowedPrincipalsSet                   map[string]bool
	allowedPrincipalAccountIdsSet          map[string]bool
	isPublic                               bool
	isShared                               bool
}

type EvaluatedStatements struct {
	allowedPrincipalFederatedIdentitiesSet map[string]bool
	allowedPrincipalServicesSet            map[string]bool
	allowedPrincipalsSet                   map[string]bool
	allowedPrincipalAccountIdsSet          map[string]bool
	allowedOrganizationIds                 map[string]bool
	publicStatementIds                     map[string]bool
	publicAccessLevels                     []string
	isPublic                               bool
	isShared                               bool
}

type EvaluatedPolicy struct {
	AccessLevel                         string   `json:"access_level"`
	AllowedOrganizationIds              []string `json:"allowed_organization_ids"`
	AllowedPrincipals                   []string `json:"allowed_principals"`
	AllowedPrincipalAccountIds          []string `json:"allowed_principal_account_ids"`
	AllowedPrincipalFederatedIdentities []string `json:"allowed_principal_federated_identities"`
	AllowedPrincipalServices            []string `json:"allowed_principal_services"`
	IsPublic                            bool     `json:"is_public"`
	PublicAccessLevels                  []string `json:"public_access_levels"`
	PublicStatementIds                  []string `json:"public_statement_ids"`
}

func EvaluatePolicy(policyContent string, userAccountId string) (EvaluatedPolicy, error) {
	evaluatedPolicy := EvaluatedPolicy{
		AccessLevel: "private",
	}

	// Check source account id which should be valid.
	re := regexp.MustCompile(`^[0-9]{12}$`)

	if !re.MatchString(userAccountId) {
		return evaluatedPolicy, fmt.Errorf("source account id is invalid: %s", userAccountId)
	}

	if policyContent == "" {
		return evaluatedPolicy, nil
	}

	policyInterface, err := canonicalPolicy(policyContent)
	if err != nil {
		return evaluatedPolicy, err
	}

	policy := policyInterface.(Policy)

	evaluatedStatements, err := evaluateStatements(policy.Statements, userAccountId)
	if err != nil {
		return evaluatedPolicy, err
	}

	evaluatedPolicy.AccessLevel = evaluateAccessLevel(evaluatedStatements)
	evaluatedPolicy.AllowedPrincipalFederatedIdentities = setToSortedSlice(evaluatedStatements.allowedPrincipalFederatedIdentitiesSet)
	evaluatedPolicy.AllowedPrincipalServices = setToSortedSlice(evaluatedStatements.allowedPrincipalServicesSet)
	evaluatedPolicy.AllowedPrincipals = setToSortedSlice(evaluatedStatements.allowedPrincipalsSet)
	evaluatedPolicy.AllowedPrincipalAccountIds = setToSortedSlice(evaluatedStatements.allowedPrincipalAccountIdsSet)
	evaluatedPolicy.AllowedOrganizationIds = setToSortedSlice(evaluatedStatements.allowedOrganizationIds)
	evaluatedPolicy.PublicStatementIds = setToSortedSlice(evaluatedStatements.publicStatementIds)
	evaluatedPolicy.PublicAccessLevels = evaluatedStatements.publicAccessLevels
	evaluatedPolicy.IsPublic = evaluatedStatements.isPublic

	return evaluatedPolicy, nil
}

func evaluateAccessLevel(statements EvaluatedStatements) string {
	if statements.isPublic {
		return "public"
	}

	if statements.isShared {
		return "shared"
	}

	return "private"
}

func evaluateStatements(statements []Statement, userAccountId string) (EvaluatedStatements, error) {
	evaluatedStatement := EvaluatedStatements{}
	publicStatementIds := map[string]bool{}
	actionSet := map[string]bool{}

	for statementIndex, statement := range statements {
		if !checkEffectValid(statement.Effect) {
			return evaluatedStatement, fmt.Errorf("element Effect is invalid - valid choices are 'Allow' or 'Deny'")
		}

		// TODO: For phase 1 - we are only interested in allow else continue with next
		if statement.Effect == "Deny" {
			continue
		}

		// Principal
		evaluatedPrinciple, err := evaluatePrincipal(statement.Principal, userAccountId)
		if err != nil {
			return evaluatedStatement, err
		}

		evaluatedStatement.allowedPrincipalFederatedIdentitiesSet = mergeSet(
			evaluatedStatement.allowedPrincipalFederatedIdentitiesSet,
			evaluatedPrinciple.allowedPrincipalFederatedIdentitiesSet,
		)

		evaluatedStatement.allowedPrincipalServicesSet = mergeSet(
			evaluatedStatement.allowedPrincipalServicesSet,
			evaluatedPrinciple.allowedPrincipalServicesSet,
		)

		evaluatedStatement.allowedPrincipalsSet = mergeSet(
			evaluatedStatement.allowedPrincipalsSet,
			evaluatedPrinciple.allowedPrincipalsSet,
		)

		evaluatedStatement.allowedPrincipalAccountIdsSet = mergeSet(
			evaluatedStatement.allowedPrincipalAccountIdsSet,
			evaluatedPrinciple.allowedPrincipalAccountIdsSet,
		)

		// Visibility
		evaluatedStatement.isPublic = evaluatedStatement.isPublic || evaluatedPrinciple.isPublic
		evaluatedStatement.isShared = evaluatedStatement.isShared || evaluatedPrinciple.isShared

		if evaluatedPrinciple.isPublic {
			sid := evaluatedSid(statement, statementIndex)
			if _, exists := publicStatementIds[sid]; !exists {
				publicStatementIds[sid] = true
			}
		}

		// Actions
		for _, action := range statement.Action {
			if _, exists := actionSet[action]; !exists {
				actionSet[action] = true
			}

		}
	}

	evaluatedStatement.publicAccessLevels = evaluateActionSet(actionSet)
	evaluatedStatement.publicStatementIds = publicStatementIds

	return evaluatedStatement, nil
}

func evaluateAction(action string) EvaluatedAction {
	evaluated := EvaluatedAction{}
	lowerAction := strings.ToLower(action)
	actionParts := strings.Split(lowerAction, ":")
	evaluated.prefix = actionParts[0]
	raw := actionParts[1]

	wildcardLocator := regexp.MustCompile(`[0-9a-z:]*(\*|\?)`)
	located := wildcardLocator.FindString(raw)

	if located == "" {
		evaluated.priviledge = raw
		return evaluated
	}

	evaluated.priviledge = located[:len(located)-1]

	// Convert Wildcards to regexp
	matcher := fmt.Sprintf("^%s$", raw)
	matcher = strings.Replace(matcher, "*", "[a-z0-9]*", len(matcher))
	matcher = strings.Replace(matcher, "?", "[a-z0-9]{1}", len(matcher))

	evaluated.matcher = matcher

	return evaluated
}

func evaluateActionSet(actionSet map[string]bool) []string {
	if _, exists := actionSet["*"]; exists {
		return []string{
			"List",
			"Permissions management",
			"Read",
			"Tagging",
			"Write",
		}
	}

	permissions := getParliamentIamPermissions()
	permissionsLength := len(permissions)
	accessLevels := map[string]bool{}

	for action := range actionSet {
		evaluatedAction := evaluateAction(action)

		// Find service
		index := sort.Search(
			permissionsLength,
			func(index int) bool {
				return permissions[index].Prefix >= evaluatedAction.prefix
			},
		)

		if index >= permissionsLength || permissions[index].Prefix != evaluatedAction.prefix {
			continue
		}

		// Find API Call
		permissionPrivilegesLength := len(permissions[index].Privileges)
		evaluatedPrivilegeLength := len(evaluatedAction.priviledge)
		permissionPrivilegesIndex := sort.Search(
			permissionPrivilegesLength,
			func(privilegesIndex int) bool {
				currentPrivilege := permissions[index].Privileges[privilegesIndex].Privilege[:evaluatedPrivilegeLength]
				currentPrivilege = strings.ToLower(currentPrivilege)
				return currentPrivilege >= evaluatedAction.priviledge
			},
		)

		if permissionPrivilegesIndex >= permissionPrivilegesLength {
			continue
		}

		if evaluatedAction.matcher == "" {
			accessLevel := permissions[index].Privileges[permissionPrivilegesIndex].AccessLevel

			if _, exists := accessLevels[accessLevel]; !exists {
				accessLevels[accessLevel] = true
			}
			continue
		}

		matcher := regexp.MustCompile(evaluatedAction.matcher)
		for {
			currentPrivilege := strings.ToLower(permissions[index].Privileges[permissionPrivilegesIndex].Privilege)
			splitIndex := int(math.Min(float64(len(currentPrivilege)), float64(evaluatedPrivilegeLength)))
			partialPriviledge := currentPrivilege[0:splitIndex]

			permissionPrivilegesIndex++

			if partialPriviledge != evaluatedAction.priviledge {
				break
			}
			if !matcher.MatchString(currentPrivilege) {
				continue
			}
			accessLevel := permissions[index].Privileges[permissionPrivilegesIndex].AccessLevel

			if _, exists := accessLevels[accessLevel]; !exists {
				accessLevels[accessLevel] = true
			}
		}
	}

	return setToSortedSlice(accessLevels)
}

func evaluatedSid(statement Statement, statementIndex int) string {
	if statement.Sid == "" {
		return fmt.Sprintf("Statement[%d]", statementIndex+1)
	}

	return statement.Sid
}

func evaluatePrincipal(principal Principal, userAccountId string) (EvaluatedPrincipal, error) {
	evaluatedPrinciple := EvaluatedPrincipal{
		allowedPrincipalFederatedIdentitiesSet: map[string]bool{},
		allowedPrincipalServicesSet:            map[string]bool{},
		allowedPrincipalsSet:                   map[string]bool{},
		allowedPrincipalAccountIdsSet:          map[string]bool{},
	}

	for principalKey, rawPrincipalItem := range principal {
		principalItems := rawPrincipalItem.([]string)

		reIsAwsAccount := regexp.MustCompile(`^[0-9]{12}$`)
		reIsAwsResource := regexp.MustCompile(`^arn:[a-z]*:[a-z]*:[a-z]*:([0-9]{12}):.*$`)

		for _, principalItem := range principalItems {
			switch principalKey {
			case "AWS":

				var account string

				if reIsAwsAccount.MatchString(principalItem) {
					account = principalItem
				} else if reIsAwsResource.MatchString(principalItem) {
					arnAccount := reIsAwsResource.FindStringSubmatch(principalItem)
					account = arnAccount[1]
				} else if principalItem == "*" {
					evaluatedPrinciple.isPublic = true
					account = principalItem
				} else {
					return evaluatedPrinciple, fmt.Errorf("unabled to parse arn: %s", principalItem)
				}

				if userAccountId != account {
					evaluatedPrinciple.isShared = true
					evaluatedPrinciple.allowedPrincipalAccountIdsSet[account] = true
				}

				evaluatedPrinciple.allowedPrincipalsSet[principalItem] = true
			case "Service":
				evaluatedPrinciple.allowedPrincipalServicesSet[principalItem] = true
			case "Federated":
				evaluatedPrinciple.allowedPrincipalFederatedIdentitiesSet[principalItem] = true
			}
		}
	}

	if len(evaluatedPrinciple.allowedPrincipalServicesSet) > 0 {
		evaluatedPrinciple.isPublic = true
	}

	return evaluatedPrinciple, nil
}

func checkEffectValid(effect string) bool {
	if effect == "Deny" || effect == "Allow" {
		return true
	}

	return false
}

func mergeSet(set1 map[string]bool, set2 map[string]bool) map[string]bool {
	if set1 == nil {
		return set2
	}
	if set2 == nil {
		return set1
	}

	for key, value := range set2 {
		set1[key] = value
	}

	return set1
}

func setToSortedSlice(set map[string]bool) []string {
	slice := make([]string, 0, len(set))
	for index := range set {
		slice = append(slice, index)
	}

	sort.Strings(slice)

	return slice
}