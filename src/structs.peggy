import { StructToken, Identifier, __, EOS, IdentifierName, AssignmentExpression, PublicToken, PrivateToken } from "../main.js"

StructStatement =
	StructToken __ id:Identifier __ "{" __ properties:StructPropertyNameAndTypeList?  __ "}" EOS {
		return {
			type: "Struct",
			id,
			properties: properties || []
		}
	}

StructPropertyNameAndTypeList
	= head:StructPropertyAssignment tail:(__ "," __ StructPropertyAssignment)* {
      return buildList(head, tail, 3);
    }

StructPropertyAssignment
  = access:StructPropertyKeywords? key:IdentifierName __ ":" __ value:AssignmentExpression {
      return { type: "Property", key: key, value: value, access };
    }

StructPropertyKeywords =
	PublicToken
	/ PrivateToken